package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/store"
)

func TestAPIResolvesShortIDsAndSuppressesHarness(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MUNINN_HOME", home)
	t.Setenv("MUNINN_NO_EMBED", "1")
	a, err := app.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	src, _, _, err := a.DB.Source("jcode", filepath.Join(home, "harness.json"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.DB.UpsertUnit(store.Unit{SourceID: src, Key: "mem_harness", Body: "quartz banana policy", ContentHash: "hash", Scope: "global", Kind: "memory", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := a.DB.GetUnit(id)
	handler := (&Server{App: a, Mu: &sync.Mutex{}}).Handler()
	for _, ref := range []string{u.SID, u.SID[:4], "jcode:mem_harness"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://localhost/api/related?id="+ref, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		handler.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("related %s: %d %s", ref, rr.Code, rr.Body)
		}
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/forget", strings.NewReader(`{"id":"`+u.SID[:4]+`"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("forget: %d %s", rr.Code, rr.Body)
	}
	var out app.ForgetResult
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Suppressed || out.ID != u.SID || !strings.Contains(out.Message, "upstream files unchanged") {
		t.Fatalf("forget response: %+v", out)
	}
	got, err := a.DB.Resolve(u.SID)
	if err != nil || got.Active {
		t.Fatalf("still active: %+v %v", got, err)
	}
}
