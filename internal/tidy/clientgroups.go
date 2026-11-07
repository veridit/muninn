package tidy

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
)

// TopicGroup reuses the indexer's Louvain concepts, never the similarity graph.
// Members are canonical unit IDs and n is the stored concept count.
type TopicGroup struct {
	ID      int64          `json:"id"`
	Members map[int64]int  `json:"-"`
	Terms   map[string]int `json:"-"`
}

type ClientGroup struct {
	ID      int64    `json:"id"`
	Scope   string   `json:"scope"`
	Label   []string `json:"label"`
	Members int      `json:"members"`
	Total   int      `json:"total_members"`
}

var numericLabel = regexp.MustCompile(`^(\d{4}-\d\d-\d\d|\d+)$`)

// readTopicGroups shares ReadGraph's read-only snapshot. Mirror memberships and
// concepts resolve to canonical identities, with one membership per group/SID.
func readTopicGroups(ctx context.Context, tx *sql.Tx, targets map[int64]int64) ([]TopicGroup, error) {
	groups := map[int64]*TopicGroup{}
	rows, err := tx.QueryContext(ctx, `SELECT community_id,unit_id,n FROM unit_communities ORDER BY community_id,unit_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var group, unit int64
		var n int
		if err = rows.Scan(&group, &unit, &n); err != nil {
			rows.Close()
			return nil, err
		}
		canonical, ok := targets[unit]
		if !ok {
			continue
		}
		if groups[group] == nil {
			groups[group] = &TopicGroup{ID: group, Members: map[int64]int{}, Terms: map[string]int{}}
		}
		groups[group].Members[canonical] = max(groups[group].Members[canonical], n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT c.community_id,uc.unit_id,t.term FROM unit_concepts uc JOIN communities c ON c.concept_id=uc.concept_id JOIN concepts t ON t.id=uc.concept_id ORDER BY c.community_id,t.term,uc.unit_id`)
	if err != nil {
		return nil, err
	}
	// Rows for one group/term are contiguous, bounding the deduplication set.
	var lastGroup int64
	var lastTerm string
	seen := map[int64]bool{}
	for rows.Next() {
		var group, unit int64
		var term string
		if err = rows.Scan(&group, &unit, &term); err != nil {
			rows.Close()
			return nil, err
		}
		if group != lastGroup || term != lastTerm {
			seen = map[int64]bool{}
			lastGroup, lastTerm = group, term
		}
		canonical, ok := targets[unit]
		g := groups[group]
		if !ok || g == nil || g.Members[canonical] == 0 || seen[canonical] {
			continue
		}
		seen[canonical] = true
		g.Terms[term]++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]TopicGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func groupLabel(g TopicGroup) []string {
	terms := make([]string, 0, len(g.Terms))
	for term := range g.Terms {
		terms = append(terms, term)
	}
	sort.Slice(terms, func(i, j int) bool {
		if g.Terms[terms[i]] != g.Terms[terms[j]] {
			return g.Terms[terms[i]] > g.Terms[terms[j]]
		}
		return terms[i] < terms[j]
	})
	return terms[:min(5, len(terms))]
}

func redactLabel(label []string) []string {
	out := make([]string, len(label))
	for i, term := range label {
		out[i] = redacted(term)
	}
	return out
}

// clientGroups counts memories, not concept occurrences, for purity and size.
func clientGroups(g Graph, o Options) ([]ClientGroup, map[int64]map[string]int) {
	scopeByID := map[int64]string{}
	sizes := map[string]int{}
	for _, u := range g.Units {
		s := TopScope(g.scopeFor(u))
		scopeByID[u.ID] = s
		sizes[s]++
	}
	selected := []ClientGroup{}
	scores := map[int64]map[string]int{}
	for _, topic := range g.Topics {
		counts := map[string]int{}
		total := 0
		for id, n := range topic.Members {
			if s, ok := scopeByID[id]; ok && n > 0 {
				counts[s]++
				total++
			}
		}
		label := groupLabel(topic)
		descriptive := false
		for _, term := range label {
			if !numericLabel.MatchString(term) {
				descriptive = true
			}
		}
		if !descriptive {
			continue
		}
		for s, count := range counts {
			if float64(count) < o.GroupPurity*float64(total) || count < max(o.GroupMin, int(math.Ceil(float64(sizes[s])*.01))) {
				continue
			}
			selected = append(selected, ClientGroup{topic.ID, s, redactLabel(label), count, total})
			for id, n := range topic.Members {
				if _, ok := scopeByID[id]; !ok {
					continue
				}
				if scores[id] == nil {
					scores[id] = map[string]int{}
				}
				scores[id][s] += n
			}
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		a, b := selected[i], selected[j]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Members != b.Members {
			return a.Members > b.Members
		}
		return a.ID < b.ID
	})
	return selected, scores
}

func topClientGroups(groups []ClientGroup, scope string) []ClientGroup {
	out := []ClientGroup{}
	for _, g := range groups {
		if g.Scope == scope {
			out = append(out, g)
			if len(out) == 5 {
				break
			}
		}
	}
	return out
}

func classRank(class string) int {
	switch class {
	case "strong":
		return 0
	case "group-only":
		return 1
	case "vote-only", "weak":
		return 2
	}
	return 3
}

func (p Plan) ClassCounts() map[string]int {
	counts := map[string]int{"strong": 0, "group-only": 0, "vote-only": 0}
	for _, e := range p.Entries {
		counts[e.Strength]++
	}
	return counts
}

type ClassTransition struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Class  string `json:"class"`
	Count  int    `json:"count"`
}

func (p Plan) ClassTransitions() []ClassTransition {
	counts := map[[3]string]int{}
	for _, e := range p.Entries {
		counts[[3]string{TopScope(effectiveSource(e)), e.Target, e.Strength}]++
	}
	out := []ClassTransition{}
	for key, n := range counts {
		out = append(out, ClassTransition{key[0], key[1], key[2], n})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return classRank(a.Class) < classRank(b.Class)
	})
	return out
}

func (p *Plan) RefreshCounts() { p.Classes = p.ClassCounts(); p.Transitions = p.ClassTransitions() }

func writeSignatures(b io.Writer, p Plan) {
	io.WriteString(b, "\n## Client-group signatures (heuristic evidence)\n")
	for _, scope := range p.Scopes {
		fmt.Fprintf(b, "\n### %s\n", scope)
		groups := topClientGroups(p.ClientGroups, scope)
		if len(groups) == 0 {
			io.WriteString(b, "\nNo qualifying client groups.\n")
		}
		for _, g := range groups {
			fmt.Fprintf(b, "\n- Group %d: %v, %d scope members / %d total\n", g.ID, g.Label, g.Members, g.Total)
		}
	}
}
