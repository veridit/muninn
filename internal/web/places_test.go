package web

import (
	"encoding/json"
	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/scopetest"
	"strings"
	"sync"
	"testing"
)

func TestWebRememberPlacesAndQuestion(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "global")
	h := (&Server{App: f.App, Mu: &sync.Mutex{}, Binding: b}).Handler()
	out, status := matrixRequest(h, "GET", "/api/places", "")
	var places []app.Place
	if status != 200 || json.Unmarshal([]byte(out), &places) != nil || len(places) != 2 {
		t.Fatal(out)
	}
	out, status = matrixRequest(h, "POST", "/api/remember", `{"content":"Synthetic work repo preference.","place":"global"}`)
	if status != 400 || !strings.Contains(out, "Are you sure it applies beyond it?") {
		t.Fatal(out)
	}
	out, status = matrixRequest(h, "POST", "/api/remember", `{"content":"Synthetic work repo preference.","place":"global","place_reason":"Synthetic rule useful in all projects."}`)
	if status != 200 {
		t.Fatal(out)
	}
	var reply struct {
		ID, Place string
		Places    []app.Place
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil || reply.Place != "global" || len(reply.Places) != 2 {
		t.Fatal(out)
	}
	u, err := f.App.DB.Resolve(reply.ID)
	if err != nil || u.Scope != "jcode@global" {
		t.Fatalf("stored: %+v %v", u, err)
	}
	out, status = matrixRequest(h, "POST", "/api/remember", `{"content":"Synthetic literal-scope rule.","place":"global","scope":"project:matrix-project"}`)
	if status != 200 {
		t.Fatal(out)
	}
	json.Unmarshal([]byte(out), &reply)
	if reply.Place != "here" {
		t.Fatal(out)
	}
}
