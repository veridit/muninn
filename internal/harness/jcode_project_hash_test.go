package harness

import "testing"

// Vectors produced by Rust's DefaultHasher over PathBuf (the function jcode's
// project_memory_file uses), so a Go drift from std fails here, not in recall.
func TestJcodeProjectHashMatchesRust(t *testing.T) {
	for path, want := range map[string]string{
		"/tmp/quartz":              "3c213a911ce813ad",
		"/tmp/quartz/":             "3c213a911ce813ad",
		"/tmp//quartz":             "3c213a911ce813ad",
		"/tmp/./quartz":            "3c213a911ce813ad",
		"~/Projects/quartz-ledger": "4887db0ca3a1dddf",
		"/":                        "bd60acb658c79e45",
		"/work/a-much-longer-directory-name/with/several/components/": "fd434aa4ab663aea",
		"./rel": "8cbb25218d97f0bb",
	} {
		if got := JcodeProjectHash(path); got != want {
			t.Errorf("JcodeProjectHash(%q) = %s, want %s", path, got, want)
		}
	}
}
