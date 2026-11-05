package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var clientName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ClientRoot walks cwd's physical ancestors for the nearest .muninn/client.
// The file holds one case-sensitive identifier, surrounded by optional whitespace.
// A .muninn directory without a client file does not stop the walk. Invalid or
// unreadable markers fail closed instead of falling back to personal memory.
func ClientRoot(cwd string) (name, root string, err error) {
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return "", "", err
		}
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return "", "", err
	}
	// Resolve the longest existing ancestor too: hook payloads can name a
	// not-yet-created directory beneath a symlink into a client workspace.
	var tail []string
	for d := cwd; ; d = filepath.Dir(d) {
		physical, e := filepath.EvalSymlinks(d)
		if e == nil {
			cwd = physical
			for i := len(tail) - 1; i >= 0; i-- {
				cwd = filepath.Join(cwd, tail[i])
			}
			break
		}
		if !errors.Is(e, os.ErrNotExist) {
			return "", "", fmt.Errorf("%s: %w", d, e)
		}
		if filepath.Dir(d) == d {
			return "", "", e
		}
		tail = append(tail, filepath.Base(d))
	}
	for d := cwd; ; d = filepath.Dir(d) {
		path := filepath.Join(d, ".muninn", "client")
		data, e := os.ReadFile(path)
		if e == nil {
			value := strings.TrimSpace(string(data))
			if !clientName.MatchString(value) {
				return "", d, fmt.Errorf("%s: invalid client name (expected letters, digits, _ or -, starting with a letter or digit)", path)
			}
			return value, d, nil
		}
		if !errors.Is(e, os.ErrNotExist) {
			return "", d, fmt.Errorf("%s: %w", path, e)
		}
		if _, statErr := os.Lstat(path); statErr == nil {
			return "", d, fmt.Errorf("%s: unreadable client marker: %w", path, e)
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return "", "", nil
}
