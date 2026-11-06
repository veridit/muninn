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
	"os"
	"strings"
	"sync"
	"time"

	"github.com/veridit/muninn/internal/app"
	embedpkg "github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
)

//go:embed static
var static embed.FS

// Server is the HTTP front end. Mu serialises writers (index, remember,
// forget) so the single SQLite writer is never contended.
type Server struct {
	App     *app.App
	Mu      *sync.Mutex
	Binding app.Binding
}

type apiHit struct {
	ID          string  `json:"id"`
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
		h := apiHit{ID: r.Unit.SID, Key: r.Unit.Key, Text: r.Unit.Body, Kind: r.Unit.Kind, Scope: r.Unit.Scope,
			Score: r.Total, Forgettable: true}
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
	b := s.Binding
	if b.Name == "" {
		cwd, _ := os.Getwd()
		b = s.App.Bound("", cwd)
	}
	if b.Err != nil {
		return localOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fail(w, http.StatusUnprocessableEntity, b.Err)
		}))
	}
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		b := b.Refresh(s.App)
		o := s.App.RecallOptions()
		o.MinScore, o.Limit, o.Scopes = 0, 20, b.Scopes
		o.ScopeFor = b.ScopeFor
		res, err := s.App.Searcher.Search(r.Context(), r.URL.Query().Get("q"), o)
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, map[string]any{"results": s.hits(res, r.URL.Query().Get("explain") == "1")})
	})
	mux.HandleFunc("GET /api/related", func(w http.ResponseWriter, r *http.Request) {
		b := b.Refresh(s.App)
		u, err := b.Resolve(s.App, r.URL.Query().Get("id"))
		if err != nil {
			fail(w, 404, err)
			return
		}
		res, err := s.App.Searcher.RelatedIn(r.Context(), u.ID, 10, b.Scopes, b.ScopeFor)
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, map[string]any{"results": s.hits(res, false)})
	})
	mux.HandleFunc("POST /api/recall", func(w http.ResponseWriter, r *http.Request) {
		b := b.Refresh(s.App)
		var in struct {
			Message, Session, Harness string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, 400, err)
			return
		}
		resp, err := s.App.Recall.Recall(r.Context(), recall.Request{Message: in.Message, Session: in.Session, Harness: in.Harness, Scopes: b.Scopes, ScopeFor: b.ScopeFor})
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, resp)
	})
	mux.HandleFunc("POST /api/remember", func(w http.ResponseWriter, r *http.Request) {
		b := b.Refresh(s.App)
		var in struct {
			Content, Category, Scope, SafeReason string
			Tags                                 []string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, 400, err)
			return
		}
		s.Mu.Lock()
		defer s.Mu.Unlock()
		u, re, err := b.RememberSafe(r.Context(), s.App, in.Content, in.Category, in.Scope, in.Tags, in.SafeReason)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, map[string]any{"id": u.SID, "reinforced": re})
	})
	mux.HandleFunc("POST /api/forget", func(w http.ResponseWriter, r *http.Request) {
		b := b.Refresh(s.App)
		var in struct{ ID string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, 400, err)
			return
		}
		s.Mu.Lock()
		defer s.Mu.Unlock()
		out, err := b.Forget(r.Context(), s.App, in.ID)
		if err != nil {
			fail(w, 404, err)
			return
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /api/injections", func(w http.ResponseWriter, r *http.Request) {
		b := b.Refresh(s.App)
		where, args := b.SQL("u.scope", "u.source_id")
		rows, err := s.App.DB.QueryContext(r.Context(), `SELECT i.session, i.harness, i.at_ns, u.body FROM injections i
		  JOIN units mirror ON mirror.id=i.unit_id JOIN units u ON u.sid=mirror.sid
		  WHERE u.id=(SELECT c.id FROM units c JOIN sources cs ON cs.id=c.source_id WHERE c.sid=u.sid ORDER BY (cs.kind='native') DESC,c.updated_ns DESC,c.id LIMIT 1)
		  AND (`+where+`) ORDER BY i.at_ns DESC LIMIT 100`, args...)
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
		b := b.Refresh(s.App)
		st, err := app.Stats(r.Context(), s.App, b)
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, st)
	})
	// Embedding for hooks: serve keeps the built-in model loaded so a hook
	// process need not load ~560 MB per prompt. GET reports the model.
	mux.HandleFunc("GET /api/embed", func(w http.ResponseWriter, r *http.Request) {
		if s.App.Embedder == nil {
			fail(w, http.StatusServiceUnavailable, errors.New("no embedder"))
			return
		}
		writeJSON(w, embedpkg.EmbedResponse{Model: s.App.Embedder.Model()})
	})
	mux.HandleFunc("POST /api/embed", func(w http.ResponseWriter, r *http.Request) {
		if s.App.Embedder == nil {
			fail(w, http.StatusServiceUnavailable, errors.New("no embedder"))
			return
		}
		var in embedpkg.EmbedRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil || len(in.Texts) > 256 {
			fail(w, 400, errors.New("want {\"texts\": [...]} with at most 256 texts"))
			return
		}
		vecs, err := s.App.Embedder.Embed(r.Context(), in.Texts)
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, embedpkg.EmbedResponse{Model: s.App.Embedder.Model(), Vectors: vecs})
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
