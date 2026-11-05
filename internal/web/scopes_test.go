package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/veridit/muninn/internal/scopetest"
	"github.com/veridit/muninn/internal/store"
)

func matrixRequest(h http.Handler, method, path, body string) (string, int) {
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:12345"
	h.ServeHTTP(rr, r)
	return rr.Body.String(), rr.Code
}

func TestInjectionHistoryUsesCanonicalBoundaryAfterRescope(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "global")
	u := f.Units["Personal"]
	if err := os.WriteFile(f.Root+"/mirror.json", []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	src, _, _, err := f.App.DB.Source("jcode", f.Root+"/mirror.json")
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.App.DB.UpsertUnit(store.Unit{SourceID: src, Key: "mem_history_mirror", Origin: u.Origin, Scope: "global", Body: u.Body, ContentHash: u.ContentHash, Kind: "memory", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.App.DB.Exec(`INSERT INTO injections(session,unit_id,content_hash,harness,at_ns) VALUES('mirror-session',?,?, 'test',1)`, id, u.ContentHash); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Rescope(context.Background(), f.App, u.SID, "client:acme", true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		binding string
		visible bool
	}{{"global", false}, {"client", true}} {
		bound := f.Bind(t, tc.binding)
		h := (&Server{App: f.App, Mu: &sync.Mutex{}, Binding: bound}).Handler()
		out, status := matrixRequest(h, "GET", "/api/injections", "")
		if status != 200 {
			t.Fatal(out)
		}
		if strings.Contains(out, u.Body) != tc.visible {
			t.Fatalf("%s history boundary: %s", tc.binding, out)
		}
	}
	scopetest.Pass(t, "web.injections-canonical-rescope", "global/client")
}
func TestScopeMatrixWeb(t *testing.T) {
	for _, binding := range []string{"alpha", "global", "client"} {
		t.Run(binding, func(t *testing.T) {
			f := scopetest.New(t)
			b := f.Bind(t, binding)
			cwd := f.Cwd
			if binding == "client" {
				cwd = f.ClientCwd
			}
			t.Chdir(cwd)
			h := (&Server{App: f.App, Mu: &sync.Mutex{}}).Handler()
			for _, tc := range []struct{ name, method, path, body string }{
				{"web.search", "GET", "/api/search?q=" + url.QueryEscape(scopetest.Query), ""},
				{"web.recall", "POST", "/api/recall", `{"message":"` + scopetest.Query + `"}`},
				{"web.injections", "GET", "/api/injections", ""},
			} {
				out, status := matrixRequest(h, tc.method, tc.path, tc.body)
				if status != 200 {
					t.Fatal(out)
				}
				f.Read(t, binding, out)
				scopetest.Pass(t, tc.name, binding)
			}
			visible := f.Units["Alpha"]
			if binding == "global" {
				visible = f.Units["Personal"]
			}
			if binding == "client" {
				visible = f.Units["Client"]
			}
			for _, ref := range []string{visible.SID, visible.SID[:4], visible.Key} {
				out, status := matrixRequest(h, "GET", "/api/related?id="+ref, "")
				if status != 200 {
					t.Fatal(out)
				}
				f.Clean(t, binding, out)
				if !strings.Contains(out, "quartz deployment") {
					t.Fatal("empty related")
				}
			}
			for _, ref := range []string{f.Units["Beta"].SID, f.Units["Beta"].SID[:4], f.Units["Beta"].Key, f.Units["OtherClient"].SID} {
				out, status := matrixRequest(h, "GET", "/api/related?id="+ref, "")
				if status != 404 || !strings.Contains(out, "no visible memory") {
					t.Fatal(out)
				}
				f.Clean(t, binding, out)
				out, status = matrixRequest(h, "POST", "/api/forget", `{"id":"`+ref+`"}`)
				if status != 404 || !strings.Contains(out, "no visible memory") {
					t.Fatal(out)
				}
				f.Clean(t, binding, out)
			}
			scopetest.Pass(t, "web.related", binding)
			out, status := matrixRequest(h, "GET", "/api/stats", "")
			if status != 200 {
				t.Fatal(out)
			}
			f.Clean(t, binding, out)
			if strings.Contains(out, "jcode@beta") || strings.Contains(out, "client:other") || !strings.Contains(out, b.Name) {
				t.Fatal(out)
			}
			if binding == "global" && (!strings.Contains(out, "engram:frogs") || !strings.Contains(out, "sql_saga") || !strings.Contains(out, "personal")) {
				t.Fatal(out)
			}
			scopetest.Pass(t, "web.stats", binding)
			out, status = matrixRequest(h, "POST", "/api/remember", `{"content":"matrix web durable fact"}`)
			if status != 200 {
				t.Fatal(out)
			}
			f.Clean(t, binding, out)
			var remembered struct{ ID string }
			if err := json.Unmarshal([]byte(out), &remembered); err != nil {
				t.Fatal(err)
			}
			u, err := f.App.DB.Resolve(remembered.ID)
			if err != nil || u.Scope != b.DefaultScope {
				t.Fatalf("default: %+v %v", u, err)
			}
			for _, scope := range []string{"jcode@beta", "client:other"} {
				out, status = matrixRequest(h, "POST", "/api/remember", `{"content":"not stored","scope":"`+scope+`"}`)
				if status != 400 {
					t.Fatalf("cross scope write: %s", out)
				}
				f.Clean(t, binding, out)
			}
			scopetest.Pass(t, "web.remember", binding)
			out, status = matrixRequest(h, "POST", "/api/forget", `{"id":"`+visible.SID[:4]+`"}`)
			if status != 200 {
				t.Fatal(out)
			}
			f.Clean(t, binding, out)
			if u, err := f.App.DB.Resolve(f.Units["Beta"].SID); err != nil || !u.Active {
				t.Fatal("beta changed")
			}
			scopetest.Pass(t, "web.forget", binding)
			// HTTP query parameters cannot silently widen the server's binding.
			out, status = matrixRequest(h, "GET", "/api/search?q="+url.QueryEscape(scopetest.Query)+"&all=1&scope=client:*", "")
			if status != 200 {
				t.Fatal(out)
			}
			f.Clean(t, binding, out)
		})
	}
}
