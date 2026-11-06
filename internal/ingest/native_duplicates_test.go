package ingest_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/ingest"
)

func TestNativeDuplicateIDsRejectedAndLegacyRepairOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.json")
	first := ingest.NativeMemory{ID: "synthetic-duplicate", Content: "Synthetic quartz private note.", Scope: "global", Active: true, Created: time.Now().UTC()}
	if err := ingest.SaveNative(path, []ingest.NativeMemory{first}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := ingest.SaveNative(path, []ingest.NativeMemory{first, first}); err == nil {
		t.Fatal("duplicate IDs accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected save changed store")
	}
	second := first
	second.Created = first.Created.Add(time.Second)
	second.Updated = second.Created
	raw, _ := json.Marshal(map[string]any{"version": 1, "memories": []ingest.NativeMemory{first, second}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ingest.LoadNative(path)
	if err != nil || len(got) != 1 || !got[0].Created.Equal(first.Created) {
		t.Fatalf("repair len=%d err=%v", len(got), err)
	}
	logPath := filepath.Join(filepath.Dir(path), "native-repairs.log")
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "removed=1") || strings.Contains(string(log), first.ID) || strings.Contains(string(log), first.Content) {
		t.Fatalf("unsafe repair log: %q", log)
	}
	if _, err := ingest.LoadNative(path); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(logPath)
	if !bytes.Equal(log, again) {
		t.Fatal("repair logged repeatedly")
	}
	t.Log("NATIVE repair: duplicate saves refused without mutation; legacy retry duplicate collapsed once, first row retained, count-only log")
}

func TestNativeLegacyCaptureCollisionRekeyPreservesPayload(t *testing.T) {
	first := ingest.NativeMemory{ID: "cap_legacy", Origin: "capture:jcode:synthetic:batch:0", Content: "Synthetic first quartz fact.", Scope: "jcode@alpha", Active: true, Strength: 2, Created: time.Unix(100, 0).UTC(), CaptureEvents: []string{"receipt-first"}}
	second := first
	second.Content, second.Category, second.Trust = "Synthetic second quartz fact.", "decision", "high"
	second.Created, second.Updated, second.Strength = time.Unix(200, 0).UTC(), time.Unix(210, 0).UTC(), 4
	second.CaptureEvents = []string{"receipt-second"}
	second.CaptureSupersedes = "old-target-sid"
	raw, _ := json.Marshal(map[string]any{"version": 1, "memories": []ingest.NativeMemory{first, second}})
	var stableID string
	for attempt := 0; attempt < 2; attempt++ {
		path := filepath.Join(t.TempDir(), "memories.json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := ingest.LoadNative(path)
		if err != nil || len(got) != 2 {
			t.Fatalf("repair rows=%d err=%v", len(got), err)
		}
		if got[0].ID != first.ID || !strings.HasPrefix(got[1].ID, "cap_repair_") || got[0].SID == got[1].SID {
			t.Fatal("repair identities not independent")
		}
		if attempt > 0 && got[1].ID != stableID {
			t.Fatal("repair ID not deterministic")
		}
		stableID = got[1].ID
		payload := got[1]
		payload.ID, payload.SID = second.ID, second.SID
		if !reflect.DeepEqual(payload, second) {
			t.Fatal("repair changed a payload field")
		}
		before, _ := os.ReadFile(path)
		if _, err := ingest.LoadNative(path); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("repair was not idempotent")
		}
		log, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "native-repairs.log"))
		if !strings.Contains(string(log), "removed=0 rekeyed=1") || strings.Contains(string(log), first.Content) || strings.Contains(string(log), first.ID) {
			t.Fatal("repair log not count-only")
		}
	}
}
func TestNativeConflictingDuplicateIDsFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.json")
	raw := []byte(`{"version":1,"memories":[{"id":"same","content":"one"},{"id":"same","content":"two"}]}`)
	os.WriteFile(path, raw, 0600)
	if _, err := ingest.LoadNative(path); err == nil {
		t.Fatal("conflicting duplicate silently discarded")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(raw, after) {
		t.Fatal("conflicting store mutated")
	}
}
