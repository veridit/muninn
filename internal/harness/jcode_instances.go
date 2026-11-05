package harness

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// JcodeInstance is one isolated jcode installation: its own JCODE_HOME with
// config.toml, memory/, sessions/, bin/ and runtime/.
type JcodeInstance struct {
	Name string // "global" for ~/.jcode, otherwise the registry name
	Root string // project root ($HOME for global)
	Home string // JCODE_HOME (<root>/.jcode)
}

// Scope is the recall scope prefix for this instance's memories.
func (i JcodeInstance) Scope() string { return "jcode@" + i.Name }

// Binary is the instance's own jcode binary, if it has one.
func (i JcodeInstance) Binary() string {
	if p := filepath.Join(i.Home, "bin", "jcode"); isExec(p) {
		return p
	}
	return ""
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

var (
	reRegistryStart = regexp.MustCompile(`^\s*(?:typeset|declare)\s+-[A-Za-z]*A[A-Za-z]*\s+JCODE_INSTANCES\s*=\s*\(`)
	reWord          = regexp.MustCompile(`"[^"]*"|'[^']*'|[^\s()]+`)
)

// ParseZshInstances reads the `typeset -gA JCODE_INSTANCES=( name root ... )`
// registry used by a per-project jcode wrapper. It is a static parse (the
// shell is never executed); $HOME and ~ are expanded.
func ParseZshInstances(path, home string) ([]JcodeInstance, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var words []string
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !in {
			if loc := reRegistryStart.FindStringIndex(line); loc != nil {
				in = true
				line = line[loc[1]:]
			} else {
				continue
			}
		}
		if i := strings.Index(line, "#"); i >= 0 && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			line = line[:i]
		}
		end := strings.Contains(line, ")")
		if end {
			line = line[:strings.Index(line, ")")]
		}
		for _, w := range reWord.FindAllString(line, -1) {
			words = append(words, strings.Trim(w, `"'`))
		}
		if end {
			break
		}
	}
	var out []JcodeInstance
	for i := 0; i+1 < len(words); i += 2 {
		root := expandHome(words[i+1], home)
		out = append(out, JcodeInstance{Name: words[i], Root: root, Home: filepath.Join(root, ".jcode")})
	}
	return out, sc.Err()
}

func expandHome(p, home string) string {
	p = strings.Replace(p, "${HOME}", home, 1)
	p = strings.Replace(p, "$HOME", home, 1)
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = home + p[1:]
	}
	return filepath.Clean(p)
}

// DiscoverJcode returns every jcode instance: the global ~/.jcode, instances
// registered in ~/.zshrc, and extra JCODE_HOME paths from config. Only
// homes that exist are returned. Duplicates (same Home) are merged.
func DiscoverJcode(home string, extraHomes []string) []JcodeInstance {
	seen := map[string]bool{}
	var out []JcodeInstance
	add := func(i JcodeInstance) {
		i.Home = filepath.Clean(i.Home)
		if seen[i.Home] {
			return
		}
		if fi, err := os.Stat(i.Home); err != nil || !fi.IsDir() {
			return
		}
		seen[i.Home] = true
		out = append(out, i)
	}
	add(JcodeInstance{Name: "global", Root: home, Home: filepath.Join(home, ".jcode")})
	for _, rc := range []string{".zshrc", ".zshenv", ".zprofile"} {
		if insts, err := ParseZshInstances(filepath.Join(home, rc), home); err == nil {
			for _, i := range insts {
				add(i)
			}
		}
	}
	for _, h := range extraHomes {
		h = expandHome(h, home)
		name := filepath.Base(filepath.Dir(h))
		add(JcodeInstance{Name: name, Root: filepath.Dir(h), Home: h})
	}
	sort.SliceStable(out[1:], func(a, b int) bool { return out[1+a].Name < out[1+b].Name })
	return out
}

// InstanceForHome finds the instance whose JCODE_HOME is h (the hook process
// environment), falling back to an ad-hoc instance named after its root.
func InstanceForHome(insts []JcodeInstance, h string) JcodeInstance {
	h = filepath.Clean(h)
	for _, i := range insts {
		if i.Home == h {
			return i
		}
	}
	// Explicit global homes must keep the same identity when discovery is off
	// (including throwaway MUNINN_HOME comparisons). Bound uses jcode@global,
	// so naming this instance after the login directory would hide its memories.
	if home, err := os.UserHomeDir(); err == nil && h == filepath.Join(home, ".jcode") {
		return JcodeInstance{Name: "global", Root: home, Home: h}
	}
	return JcodeInstance{Name: filepath.Base(filepath.Dir(h)), Root: filepath.Dir(h), Home: h}
}
