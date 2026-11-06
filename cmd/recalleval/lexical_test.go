// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"fmt"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
	"path/filepath"
	"strings"
	"testing"
)

func TestLexicalCallbackBindingBeforeBudgetAndChronology(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	src, _, _, err := db.Source("native", filepath.Join(t.TempDir(), "mem.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		scope, body := "private", "quartz quartz quartz"
		if i == 19 {
			scope, body = "remappable", "quartz note"
		}
		_, err = db.UpsertUnit(store.Unit{SourceID: src, Key: fmt.Sprint(i), ContentHash: fmt.Sprint(i), Kind: "memory", Body: body, Scope: scope, Active: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	fn := lexicalCallback(context.Background(), db, "bm25")
	binding := func(u store.Unit) (string, bool) {
		if u.Scope == "remappable" {
			return "bound", true
		}
		return "", false
	}
	opts := search.Options{Limit: 1, Scopes: []string{"bound"}, ScopeFor: binding}
	rs, err := fn(context.Background(), "quartz", opts)
	if err != nil || len(rs) != 1 || rs[0].Unit.Scope != "bound" || rs[0].Unit.Key != "19" {
		t.Fatalf("binding starved or lost: %+v %v", rs, err)
	}
	calls := 0
	e := recall.Engine{DB: db, Options: opts, SearchFunc: func(c context.Context, q string, o search.Options) ([]search.Result, error) {
		calls++
		return fn(c, q, o)
	}}
	own, err := e.Recall(context.Background(), recall.Request{Message: recall.Marker + " quartz"})
	if err != nil || own.Skipped != "own injection" || calls != 0 {
		t.Fatalf("own output reached callback: %+v %v", own, err)
	}
	request := recall.Request{Session: "test", Message: "quartz", Scopes: []string{"bound"}, ScopeFor: binding}
	first, err := e.Recall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Recall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Hits) != 1 || len(second.Hits) != 0 || !strings.Contains(first.Block, "quartz note") {
		t.Fatalf("chronology broken: %+v %+v", first, second)
	}
	request.Session = "other"
	other, err := e.Recall(context.Background(), request)
	if err != nil || len(other.Hits) != 1 {
		t.Fatalf("session isolation: %+v %v", other, err)
	}
}
