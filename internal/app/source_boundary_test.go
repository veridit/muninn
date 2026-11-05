package app_test

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/scopetest"
)

func TestPhysicalSourceBoundaryResolverAndForget(t *testing.T) {
	for _, binding := range []string{"alpha", "global", "client"} {
		t.Run(binding, func(t *testing.T) {
			f := scopetest.New(t)
			b := f.Bind(t, binding)
			for _, name := range []string{"ClientNote", "OutsideNote", "OtherClientNote", "ClientHarness", "ClientFile"} {
				u := f.Units[name]
				want := binding == "client" && name != "OutsideNote" && name != "OtherClientNote" || binding == "global" && name == "OutsideNote"
				for _, ref := range []string{u.SID, u.SID[:4], "#" + strconv.FormatInt(u.ID, 10)} {
					got, err := b.Resolve(f.App, ref)
					if (err == nil) != want {
						t.Fatalf("%s %s: visible=%v err=%v", binding, name, want, err)
					}
					if want && got.SID != u.SID {
						t.Fatal("physical binding changed identity")
					}
					if !want {
						if _, err := b.Forget(context.Background(), f.App, ref); err == nil {
							t.Fatalf("forgot hidden %s", name)
						}
					}
				}
			}
			scopetest.Pass(t, "source-path.resolve-forget", binding)
		})
	}
}

func TestExternalImportRequiresExplicitClientMigration(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "client")
	items := []ingest.Imported{{Origin: "external:unmarked", Content: "external durable fact", Scope: "client:acme", Active: true}}
	if _, err := b.MergeImport(app.NativePath(), items, false); err == nil {
		t.Fatal("unprovenanced input entered client")
	}
	// A physical jcode source under the root has explicit client provenance.
	items[0].Origin = "jcode@alpha:project/stable"
	items[0].Scope = "jcode@alpha:project"
	src := filepath.Join(f.Root, "client", "jcode", "memory")
	st, err := b.MergeImport(app.NativePath(), items, false, src)
	if err != nil || st.Added != 1 {
		t.Fatalf("rooted import: %+v %v", st, err)
	}
	if _, err := f.App.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	mems, err := ingest.LoadNative(app.NativePath())
	if err != nil {
		t.Fatal(err)
	}
	var sid string
	for _, m := range mems {
		if m.Origin == items[0].Origin {
			if m.Scope != "client:acme" {
				t.Fatal(m.Scope)
			}
			sid = m.SID
		}
	}
	st, err = b.MergeImport(app.NativePath(), items, false, src)
	if err != nil || st.Added != 0 || st.Updated != 0 {
		t.Fatalf("rerun: %+v %v", st, err)
	}
	u, err := b.Resolve(f.App, sid)
	if err != nil || u.SID != sid {
		t.Fatalf("identity: %+v %v", u, err)
	}
	scopetest.Pass(t, "import.physical-provenance", "client")
}
