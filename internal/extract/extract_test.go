package extract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const validLine = `{"category":"preference","content":"Use tabs in Go.","trust":"high"}`

func TestParseRobustness(t *testing.T) {
	secret := "sk-" + strings.Repeat("z", 30)
	cases := []struct {
		name, text string
		want, bad  int
	}{
		{"valid", validLine, 1, 0},
		{"garbage-between", "garbage\n" + validLine + "\n{\"category\":", 1, 2},
		{"partial-line", `{"category":"fact"`, 0, 1},
		{"unknown-fields", strings.TrimSuffix(validLine, "}") + `,"scope":"global"}`, 0, 1},
		{"unknown-trust", strings.ReplaceAll(validLine, "high", "root"), 0, 1},
		{"trailing-garbage", validLine + " garbage", 0, 1},
		{"secret", strings.ReplaceAll(validLine, "Use tabs in Go.", secret), 0, 1},
		{"array", "[" + validLine + "]", 0, 1},
		{"long-line", strings.Repeat("x", 70<<10), 0, 1},
		{"long-line-recovery", strings.Repeat("x", 70<<10) + "\n" + validLine, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, bad := Parse(tc.text)
			if len(got) != tc.want || bad != tc.bad {
				t.Fatalf("count=%d bad=%d", len(got), bad)
			}
		})
	}
	t.Log("CAPTURE parser: complete valid lines retained, garbage/partial/secret lines dropped")
}

func TestRunFakeLLMAndRedactedErrors(t *testing.T) {
	t.Setenv("CAPTURE_TEST_KEY", "synthetic-key")
	status := 200
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Error("request contract")
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "fake" || len(request.Messages) != 2 || !strings.Contains(request.Messages[1].Content, "known-sid") {
			t.Error("request payload")
		}
		w.WriteHeader(status)
		if status != 200 {
			w.Write([]byte("synthetic-key"))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "garbage\n" + validLine}, "finish_reason": "stop"}}, "usage": Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}})
	}))
	defer s.Close()
	c := Defaults()
	c.BaseURL = s.URL
	c.APIKeyEnv = "CAPTURE_TEST_KEY"
	c.Model = "fake"
	out, err := Run(context.Background(), c, "user: I prefer tabs", []Known{{SID: "known-sid", Content: "Known fact"}})
	if err != nil || len(out.Candidates) != 1 || out.Rejected != 1 || out.Usage.TotalTokens != 15 {
		t.Fatalf("result=%+v err=%v", out, err)
	}
	status = 401
	_, err = Run(context.Background(), c, "user: test", []Known{{SID: "known-sid"}})
	if err == nil || strings.Contains(err.Error(), "synthetic-key") {
		t.Fatal("provider error not redacted")
	}
}

func TestTimeoutAndOff(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); time.Sleep(200 * time.Millisecond) }))
	defer s.Close()
	c := Defaults()
	c.Provider = "ollama"
	c.BaseURL = s.URL
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, c, "hello", nil)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("unbounded request: %v", err)
	}
	c.Provider = "off"
	_, err = Run(context.Background(), c, "hello", nil)
	if err != nil || requests.Load() != 1 {
		t.Fatal("off called server")
	}
	t.Log("CAPTURE timeout: caller returned within 1s; off made zero additional requests")
}

func TestPrivateEnvFile(t *testing.T) {
	c := Defaults()
	c.APIKeyEnv = "CAPTURE_FILE_TEST"
	c.EnvFile = filepath.Join(t.TempDir(), "keys.env")
	if err := os.WriteFile(c.EnvFile, []byte("CAPTURE_FILE_TEST='synthetic-value'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	k, err := key(c)
	if err != nil || k != "synthetic-value" {
		t.Fatal("private env file not read")
	}
	if os.Getenv(c.APIKeyEnv) != "" {
		t.Fatal("mutated environment")
	}
	os.Chmod(c.EnvFile, 0644)
	if _, err := key(c); err == nil {
		t.Fatal("accepted public env file")
	}
}

func FuzzParse(f *testing.F) {
	f.Add(validLine)
	f.Add("{}\n{\n")
	f.Fuzz(func(t *testing.T, s string) { Parse(s) })
}
