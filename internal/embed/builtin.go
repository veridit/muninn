package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/rostamlabs/rembed"
)

// Builtin is the embedding model muninn runs in-process (pure Go, no Ollama,
// no cgo). It is downloaded once, pinned to an exact Hugging Face revision
// and verified file by file before it is loaded.
//
// Choice measured on muninn's own benchmark (200 real title->content
// queries over 2201 memories plus 14 chit-chat probes): snowflake-arctic-
// embed-s separates true matches from chit-chat by +0.012 cosine (nomic via
// Ollama: +0.005), recall@1 0.710, recall@10 0.925, ~37 ms per query on an
// M1 Max, 127 MB of weights, Apache-2.0.
var Builtin = BuiltinModel{
	Name:     "builtin:snowflake-arctic-embed-s",
	Repo:     "Snowflake/snowflake-arctic-embed-s",
	Revision: "e596f507467533e48a2e17c007f0e1dacc837b33",
	Files: map[string]string{
		"config.json":           "4e519aa92ec40943356032afe458c8829d70c5766b109e4a57490b82f72dcfb7",
		"modules.json":          "84e40c8e006c9b1d6c122e02cba9b02458120b5fb0c87b746c41e0207cf642cf",
		"1_Pooling/config.json": "83eeb6d0c128b377a452f121f8c5a19bbbf9322092a87b856ef782f4ad2326b9",
		"tokenizer_config.json": "9ca59277519f6e3692c8685e26b94d4afca2d5438deff66483db495e48735810",
		"vocab.txt":             "07eced375cec144d27c900241f3e339478dec958f92fddbc551f295c992038a3",
		"model.safetensors":     "0350e62666ee2403db0223fc7cef6293c951cc63d1d2e71468da9a5aea3b544a",
	},
}

// BuiltinModel describes a pinned, verifiable model download.
type BuiltinModel struct {
	Name, Repo, Revision string
	Files                map[string]string // relative path -> sha256
}

// Dir is where the model lives under muninn's data directory.
func (b BuiltinModel) Dir(dataDir string) string {
	return filepath.Join(dataDir, "models", filepath.Base(b.Repo)+"@"+b.Revision[:12])
}

// Present reports whether the model was downloaded and verified (the
// .verified marker is written only after every file's sha256 matched).
func (b BuiltinModel) Present(dataDir string) bool {
	fi, err := os.Stat(filepath.Join(b.Dir(dataDir), ".verified"))
	return err == nil && !fi.IsDir()
}

// Download fetches and verifies the model. progress may be nil.
func (b BuiltinModel) Download(ctx context.Context, dataDir string, progress io.Writer) error {
	dir := b.Dir(dataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	for rel, want := range b.Files {
		dst := filepath.Join(dir, rel)
		if got, err := fileSHA(dst); err == nil && got == want {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		url := fmt.Sprintf("https://huggingface.co/%s/resolve/%s/%s", b.Repo, b.Revision, rel)
		if progress != nil {
			fmt.Fprintf(progress, "downloading %s\n", rel)
		}
		if err := download(ctx, client, url, dst, want); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
	}
	return os.WriteFile(filepath.Join(dir, ".verified"), []byte(b.Revision+"\n"), 0o644)
}

// Verify re-hashes every file.
func (b BuiltinModel) Verify(dataDir string) error {
	for rel, want := range b.Files {
		got, err := fileSHA(filepath.Join(b.Dir(dataDir), rel))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%s: sha256 %s, want %s", rel, got, want)
		}
	}
	return nil
}

func download(ctx context.Context, c *http.Client, url, dst, want string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(tmp)
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, want)
	}
	return os.Rename(tmp, dst)
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ErrNotDownloaded means the built-in model has not been fetched yet.
var ErrNotDownloaded = errors.New("built-in embedding model not downloaded (run `muninn model download`)")

// Local runs the built-in model in this process. Loading takes ~0.3-0.6 s
// and ~560 MB of RAM, so it is loaded lazily on first use; `muninn serve`
// keeps one loaded for every hook.
type Local struct {
	model   BuiltinModel
	dataDir string
	once    sync.Once
	e       *rembed.Embedder
	err     error
}

// NewLocal returns the built-in embedder, or ErrNotDownloaded.
func NewLocal(dataDir string) (*Local, error) {
	if !Builtin.Present(dataDir) {
		return nil, ErrNotDownloaded
	}
	return &Local{model: Builtin, dataDir: dataDir}, nil
}

func (l *Local) Model() string { return l.model.Name }

func (l *Local) load() error {
	l.once.Do(func() {
		// One Embed call fans out over cores; cap so a long index pass does
		// not starve the machine.
		l.e, l.err = rembed.Load(l.model.Dir(l.dataDir), rembed.WithWorkers(max(1, runtime.NumCPU()-2)))
	})
	return l.err
}

// Embed returns unit vectors.
func (l *Local) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := l.load(); err != nil {
		return nil, err
	}
	vecs, err := l.e.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	for i, v := range vecs {
		vecs[i] = Normalize(v)
	}
	return vecs, nil
}
