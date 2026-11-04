package embed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A download whose bytes do not match the pinned sha256 must never be
// marked verified or loaded.
func TestBuiltinDownloadVerifiesHashes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered"))
	}))
	defer srv.Close()
	m := BuiltinModel{Name: "test", Repo: "x/y", Revision: "0123456789abcdef", Files: map[string]string{"a.txt": strings.Repeat("0", 64)}}
	dir := t.TempDir()
	// Point downloads at the test server by pre-seeding nothing and using a
	// client that rewrites the host.
	err := download(context.Background(), srv.Client(), srv.URL+"/a.txt", filepath.Join(dir, "a.txt"), m.Files["a.txt"])
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("want sha256 mismatch, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("tampered file must not be kept")
	}
	if m.Present(dir) {
		t.Fatal("model must not count as present")
	}
}

func TestNewLocalNeedsDownload(t *testing.T) {
	if _, err := NewLocal(t.TempDir()); err != ErrNotDownloaded {
		t.Fatalf("want ErrNotDownloaded, got %v", err)
	}
}
