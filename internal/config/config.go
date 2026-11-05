// Package config resolves muninn's directories and settings.
//
// Data and config live in the OS-standard per-user locations, never in the
// repository: $XDG_DATA_HOME / ~/Library/Application Support / %AppData%.
// MUNINN_HOME overrides everything (used by tests).
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// Config is the user-editable settings file (config.json in ConfigDir).
type Config struct {
	// Embedder selects the embedding provider: "auto" (default: Ollama when
	// it answers and has EmbedModel, else the built-in model once
	// downloaded), "builtin", "ollama", or "none" (keyword-only).
	Embedder       string `json:"embedder"`
	OllamaURL      string `json:"ollama_url"`
	EmbedModel     string `json:"embed_model"`
	EmbedDelayMS   int    `json:"embed_delay_ms"`
	JcodeMemoryDir string `json:"jcode_memory_dir"` // deprecated: use jcode_homes
	// JcodeHomes are extra JCODE_HOME directories beyond ~/.jcode and the
	// JCODE_INSTANCES registry found in ~/.zshrc.
	JcodeHomes []string `json:"jcode_homes,omitempty"`
	// JcodeDiscover turns off ~/.zshrc instance discovery when false.
	JcodeDiscover *bool `json:"jcode_discover,omitempty"`
	// JcodeShareGlobal is retained for config compatibility only. Deprecated:
	// unified binding never shares the personal jcode instance into clients.
	JcodeShareGlobal bool     `json:"jcode_share_global,omitempty"`
	NotesRoots       []string `json:"notes_roots"`
	// Recall tuning.
	MinScore       float64 `json:"min_score"`
	MaxResults     int     `json:"max_results"`
	SemanticWeight float64 `json:"semantic_weight"`
	SemanticFloor  float64 `json:"semantic_floor"`
	SemanticSlots  int     `json:"semantic_slots"`
	// RelativeFloor drops recall hits scoring below this fraction of the top
	// hit, so one strong match does not drag weak neighbours along.
	RelativeFloor float64 `json:"relative_floor"`
	// RecallMinSemantic is the cosine a memory must reach to be injected
	// automatically. Measured on real memories with nomic-embed-text:
	// unrelated text lands at 0.52-0.62, true matches at 0.66-0.77.
	// Ignored when no embedder is available (keyword-only fallback).
	RecallMinSemantic float64 `json:"recall_min_semantic"`
	// RecallMinSemanticFull applies instead when every query word occurs in
	// the memory ("what indentation" scored 0.633 against the right memory).
	RecallMinSemanticFull float64 `json:"recall_min_semantic_full"`
	// The built-in model scores on its own scale, so it has its own gates
	// (measured: chit-chat max 0.659, true-match p10 0.672 over 2201 memories).
	BuiltinMinSemantic     float64 `json:"builtin_min_semantic"`
	BuiltinMinSemanticFull float64 `json:"builtin_min_semantic_full"`
	ListenAddr             string  `json:"listen_addr"`
}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		Embedder:               "auto",
		OllamaURL:              "http://127.0.0.1:11434",
		EmbedModel:             "nomic-embed-text",
		JcodeMemoryDir:         "",
		MinScore:               6.0,
		MaxResults:             5,
		SemanticWeight:         10.0, // seid search: semantic_weight
		SemanticFloor:          0.5,
		SemanticSlots:          3,    // seid search: reserved semantic slots
		RelativeFloor:          0.62, // e2e: weak neighbours land at 0.51-0.60 of the top hit
		RecallMinSemantic:      0.64,
		RecallMinSemanticFull:  0.58,
		BuiltinMinSemantic:     0.665,
		BuiltinMinSemanticFull: 0.62,
		ListenAddr:             "127.0.0.1:7431",
	}
}

// Home returns the MUNINN_HOME override, if set.
func Home() string { return os.Getenv("MUNINN_HOME") }

// DataDir holds the database.
func DataDir() string {
	if h := Home(); h != "" {
		return filepath.Join(h, "data")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "muninn")
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "muninn")
	case "windows":
		if a := os.Getenv("LOCALAPPDATA"); a != "" {
			return filepath.Join(a, "muninn")
		}
	}
	return filepath.Join(home, ".local", "share", "muninn")
}

// ConfigDir holds config.json.
func ConfigDir() string {
	if h := Home(); h != "" {
		return filepath.Join(h, "config")
	}
	d, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, ".config")
	}
	return filepath.Join(d, "muninn")
}

// DBPath is the SQLite database location.
func DBPath() string { return filepath.Join(DataDir(), "muninn.db") }

// Load reads config.json over the defaults. A missing file is not an error.
func Load() (Config, error) {
	c := Defaults()
	b, err := os.ReadFile(filepath.Join(ConfigDir(), "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}

// Save writes config.json.
func Save(c Config) error {
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(filepath.Join(ConfigDir(), "config.json"), append(b, '\n'), 0o600)
}
