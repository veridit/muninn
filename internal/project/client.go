package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var clientName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ClientRoot walks cwd's physical ancestors for the nearest .muninn-client.
// The deliberately small TOML subset is one name = "identifier" assignment,
// with blank lines and full-line comments permitted. Invalid markers fail closed.
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
	if physical, e := filepath.EvalSymlinks(cwd); e == nil {
		cwd = physical
	}
	for d := cwd; ; d = filepath.Dir(d) {
		data, e := os.ReadFile(filepath.Join(d, ".muninn-client"))
		if e == nil {
			var assignment string
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				if assignment != "" {
					return "", d, fmt.Errorf("invalid .muninn-client")
				}
				assignment = line
			}
			k, v, ok := strings.Cut(assignment, "=")
			value, e := strconv.Unquote(strings.TrimSpace(v))
			if !ok || strings.TrimSpace(k) != "name" || e != nil || !clientName.MatchString(value) {
				return "", d, fmt.Errorf("invalid .muninn-client: expected name = \"identifier\"")
			}
			return value, d, nil
		}
		if !errors.Is(e, os.ErrNotExist) {
			return "", d, e
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return "", "", nil
}
