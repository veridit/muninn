// Package web serves the local web UI and JSON API. It binds to loopback only.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
)

//go:embed static
var static embed.FS

// Server is the HTTP front end. Mu serialises writers (index, remember,
// forget) so the single SQLite writer is never contended.
type Server struct {
	App *app.App
	Mu  *sync.Mutex
}

type apiHit struct {
	ID          int64   `json:"id"`
	Key         string  `json:"key"`
	Text        string  `json:"text"`
	Kind        string  `json:"kind"`
	Scope       string  `json:"scope"`
	Score       float64 `json:"score"`
	Why         string  `json:"why,omitempty"`
	Forgettable bool    `json:"forgettable"`
}

func (s *Server) hits(res []search.Result, explain bool) []apiHit {
	out := []apiHit{}
	for _, r := range res {
		h := apiHit{ID: r.Unit.ID, Key: r.Unit.Key, Text: r.Unit.Body, Kind: r.Unit.Kind, Scope: r.Unit.Scope,
			Score: r.Total, Forgettable: strings.HasPrefix(r.Unit.Key, "mem_") && s.isNative(r.Unit.SourceID)}
		if explain {
			h.Why = r.Explain()
		}
		out = append(out, h)
	}
	return out
}

func (s *Server) isNative(sourceID int64) bool {
	var kind string
	s.App.DB.QueryRow(`SELECT kind FROM sources WHERE id=?`, sourceID).Scan(&kind)
	return kind == "native"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) { http.Error(w, err.Error(), code) }

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		o := s.App.RecallOptions()
		o.MinScore, o.Limit = 0, 20
		res, err := s.App.Searcher.Search(r.Context(), r.URL.Query().Get("q"), o)
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, map[string]any{"results": s.hits(res, r.URL.Query().Get("explain") == "1")})
	})
	mux.HandleFunc("GET /api/related", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		res, err := s.App.Searcher.Related(r.Context(), id, 10)
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, map[string]any{"results": s.hits(res, false)})
	})
	mux.HandleFunc("POST /api/recall", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Message, Session, Harness string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, 400, err)
			return
		}
		resp, err := s.App.Recall.Recall(r.Context(), recall.Request{Message: in.Message, Session: in.Session, Harness: in.Harness})
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, resp)
	})
	mux.HandleFunc("POST /api/remember", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Content, Category, Scope string
			Tags                     []string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, 400, err)
			return
		}
		s.Mu.Lock()
		defer s.Mu.Unlock()
		m, re, err := ingest.Remember(app.NativePath(), in.Content, in.Category, in.Scope, in.Tags)
		if err != nil {
			fail(w, 400, err)
			return
		}
		if _, err := s.App.Reindex(r.Context()); err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, map[string]any{"id": m.ID, "reinforced": re})
	})
	mux.HandleFunc("POST /api/forget", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ ID string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, 400, err)
			return
		}
		s.Mu.Lock()
		defer s.Mu.Unlock()
		ok, err := ingest.Forget(app.NativePath(), in.ID)
		if err != nil || !ok {
			fail(w, 404, errors.New("no such muninn memory"))
			return
		}
		s.App.Reindex(r.Context())
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/injections", func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.App.DB.QueryContext(r.Context(), `SELECT i.session, i.harness, i.at_ns, COALESCE(u.body,'') FROM injections i
		  LEFT JOIN units u ON u.id = i.unit_id ORDER BY i.at_ns DESC LIMIT 100`)
		if err != nil {
			fail(w, 500, err)
			return
		}
		defer rows.Close()
		type row struct{ Session, Harness, At, Text string }
		out := []row{}
		for rows.Next() {
			var x row
			var at int64
			rows.Scan(&x.Session, &x.Harness, &at, &x.Text)
			x.At = time.Unix(0, at).Format("2006-01-02 15:04:05")
			out = append(out, x)
		}
		writeJSON(w, map[string]any{"rows": lower(out)})
	})
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		st, err := app.Stats(r.Context(), s.App)
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, st)
	})
	return localOnly(mux)
}

// lower re-keys rows with lowercase JSON names for the page script.
func lower[T any](rows []T) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		b, _ := json.Marshal(r)
		var m map[string]any
		json.Unmarshal(b, &m)
		lm := map[string]any{}
		for k, v := range m {
			lm[strings.ToLower(k)] = v
		}
		out = append(out, lm)
	}
	return out
}

// localOnly rejects non-loopback clients and cross-site writes (DNS rebinding
// and CSRF from a browser tab).
func localOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			http.Error(w, "loopback only", http.StatusForbidden)
			return
		}
		hn := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			hn = h
		}
		if hn != "localhost" && hn != "127.0.0.1" && hn != "::1" {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			if o := r.Header.Get("Origin"); o != "" && !strings.HasPrefix(o, "http://127.0.0.1:") && !strings.HasPrefix(o, "http://localhost:") {
				http.Error(w, "cross-origin write refused", http.StatusForbidden)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// ListenAndServe serves until ctx ends. addr must be a loopback address.
func ListenAndServe(ctx context.Context, addr string, h http.Handler) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("muninn only listens on loopback (127.0.0.1 / ::1 / localhost)")
	}
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); srv.Shutdown(context.Background()) }()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
