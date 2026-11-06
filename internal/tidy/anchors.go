package tidy

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/veridit/muninn/internal/project"
	"github.com/veridit/muninn/internal/store"
)

var wordRE = regexp.MustCompile(`[\pL][\pL\pN_-]{2,}`)
var ticketRE = regexp.MustCompile(`(?i)\b([a-z][a-z0-9_]{1,30})-[0-9]+\b`)
var hostRE = regexp.MustCompile(`(?i)\b[a-z0-9](?:[a-z0-9-]*\.)+[a-z][a-z0-9-]+\b`)

func terms(text string) map[string]bool {
	text = strings.ToLower(redacted(text))
	out := map[string]bool{}
	for _, t := range wordRE.FindAllString(text, -1) {
		out[t] = true
	}
	for _, m := range ticketRE.FindAllStringSubmatch(text, -1) {
		out[m[1]+"-#"] = true
	}
	for _, t := range hostRE.FindAllString(text, -1) {
		out[t] = true
	}
	return out
}

// DeriveAnchors uses document frequency rather than raw repetitions. A term
// needs three memories, >=85% in one scope and at least 4x its outside rate.
// Ticket numbers collapse to a prefix, allowing previously unseen tickets.
// Roots supply observed repository/folder names, not a hand-written vocabulary.
func DeriveAnchors(units []store.Unit, roots map[string][]string) map[string][]string {
	sizes := map[string]int{}
	freq := map[string]map[string]int{}
	total := map[string]int{}
	for _, u := range units {
		s := TopScope(u.Scope)
		sizes[s]++
		if freq[s] == nil {
			freq[s] = map[string]int{}
		}
		for t := range terms(u.Title + " " + u.Body) {
			freq[s][t]++
			total[t]++
		}
	}
	type scored struct {
		term  string
		score float64
	}
	out := map[string][]string{}
	for s, counts := range freq {
		var candidates []scored
		for t, n := range counts {
			other := total[t] - n
			if n < 3 || float64(n)/float64(total[t]) < .85 {
				continue
			}
			inside := float64(n) / float64(sizes[s])
			outside := float64(other+1) / float64(max(1, len(units)-sizes[s]))
			if inside < 4*outside {
				continue
			}
			score := float64(n) * math.Log(1+float64(len(units))/float64(total[t])) * inside
			if strings.HasSuffix(t, "-#") || strings.Contains(t, ".") {
				score *= 3
			}
			candidates = append(candidates, scored{t, score})
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].score == candidates[j].score {
				return candidates[i].term < candidates[j].term
			}
			return candidates[i].score > candidates[j].score
		})
		for _, c := range candidates[:min(64, len(candidates))] {
			out[s] = append(out[s], c.term)
		}
	}
	// A folder name is only an anchor if observed on disk and in this scope's
	// corpus, and not shared with another root's scope. No client names are pinned.
	owners := map[string]map[string]bool{}
	for s, paths := range roots {
		for _, p := range paths {
			t := strings.ToLower(filepath.Base(p))
			if len(t) < 3 {
				continue
			}
			if owners[t] == nil {
				owners[t] = map[string]bool{}
			}
			owners[t][TopScope(s)] = true
		}
	}
	for t, ss := range owners {
		if len(ss) != 1 {
			continue
		}
		for s := range ss {
			if freq[s][t] > 0 {
				out[s] = append(out[s], t)
			}
		}
	}
	for s, ts := range out {
		sort.Strings(ts)
		uniq := ts[:0]
		for _, t := range ts {
			if len(uniq) == 0 || uniq[len(uniq)-1] != t {
				uniq = append(uniq, t)
			}
		}
		out[s] = uniq
	}
	return out
}
func MatchAnchors(text string, anchors map[string][]string) map[string][]string {
	ts := terms(text)
	out := map[string][]string{}
	for s, as := range anchors {
		for _, a := range as {
			if ts[a] {
				out[s] = append(out[s], a)
			}
		}
	}
	return out
}

// DiscoverRoots walks shallow workspace directories only. No marker is created,
// no symlink is followed and heavyweight dependency/generated trees are skipped.
func DiscoverRoots(bases []string) map[string][]string {
	out := map[string][]string{}
	seen := map[string]bool{}
	budget := 4000
	var walk func(string, int)
	walk = func(dir string, depth int) {
		if depth > 4 || budget <= 0 || seen[dir] {
			return
		}
		seen[dir] = true
		budget--
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return
		}
		client, root, err := project.ClientRoot(dir)
		if err != nil {
			return
		}
		if root == dir {
			out["client:"+client] = append(out["client:"+client], dir)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			if p, e := project.Find(dir, false); e == nil {
				s := TopScope(p.Scope())
				out[s] = append(out[s], dir)
			}
			return
		}
		if _, err := os.Stat(filepath.Join(dir, ".muninn", "project")); err == nil {
			if p, e := project.Find(dir, false); e == nil {
				s := TopScope(p.Scope())
				out[s] = append(out[s], dir)
			}
		}
		children, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, c := range children {
			n := c.Name()
			if !c.IsDir() || strings.HasPrefix(n, ".") {
				continue
			}
			switch n {
			case "node_modules", "vendor", "target", "build", "dist", "Library", "Applications", "Downloads", "Pictures", "Music", "Movies":
				continue
			}
			walk(filepath.Join(dir, n), depth+1)
		}
	}
	for _, base := range bases {
		walk(filepath.Clean(base), 0)
	}
	return out
}
