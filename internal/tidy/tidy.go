// Package tidy implements owner-initiated curation, never automatic recall.
package tidy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/veridit/muninn/internal/project"
	"github.com/veridit/muninn/internal/store"
)

type Options struct {
	GroupPurity float64 `json:"group_purity"`
	GroupMin    int     `json:"group_min"`
	GroupFloor  float64 `json:"group_floor"`
	GroupScore  int     `json:"group_score"`
	MinSim      float64 `json:"min_sim"`
	Majority    int     `json:"majority"`
	OwnMax      int     `json:"own_max"`
}

func Defaults() Options {
	return Options{MinSim: .70, Majority: 10, OwnMax: 3, GroupPurity: 2.0 / 3, GroupMin: 5, GroupFloor: .0024, GroupScore: 3}
}
func (o Options) Validate() error {
	if math.IsNaN(o.GroupFloor) || math.IsInf(o.GroupFloor, 0) || o.GroupFloor < 0 || o.GroupFloor > 1 {
		return fmt.Errorf("--group-floor must be between 0 and 1")
	}
	if math.IsNaN(o.GroupPurity) || math.IsInf(o.GroupPurity, 0) || o.GroupPurity <= .5 || o.GroupPurity > 1 {
		return fmt.Errorf("--group-purity must be greater than 0.5 and at most 1")
	}
	if math.IsNaN(o.MinSim) || math.IsInf(o.MinSim, 0) || o.MinSim < 0 || o.MinSim > 1 || o.Majority < 1 || o.Majority > 16 || o.OwnMax < 0 || o.GroupMin < 1 || o.GroupScore < 1 {
		return fmt.Errorf("invalid tidy thresholds")
	}
	return nil
}

// TopScope folds client child scopes to client:<name>, all others to their
// first colon-separated component, as used by client-group counts and votes.
func TopScope(s string) string {
	if strings.HasPrefix(s, "folder:") {
		return s // each folder is its own place; paths may hold ':'
	}
	if strings.HasPrefix(s, "client:") {
		p := strings.SplitN(s, ":", 3)
		return p[0] + ":" + p[1]
	}
	s, _, _ = strings.Cut(s, ":")
	return s
}

type Neighbor struct {
	SID   string  `json:"sid"`
	Scope string  `json:"scope"`
	Sim   float64 `json:"sim"`
	Text  string  `json:"text"`
}
type Entry struct {
	ConcreteVotes      map[string]int      `json:"concrete_votes"`
	EffectiveSource    string              `json:"effective_source,omitempty"`
	GroupScores        map[string]int      `json:"group_scores"`
	OwnClientGroups    []ClientGroup       `json:"own_client_groups"`
	TargetClientGroups []ClientGroup       `json:"target_client_groups"`
	SID                string              `json:"sid"`
	Origin             string              `json:"origin"`
	Fingerprint        string              `json:"fingerprint"`
	Source             string              `json:"source"`
	Target             string              `json:"target"`
	Text               string              `json:"text"` // redacted preview only, never used for writes
	Strength           string              `json:"strength"`
	Votes              map[string]int      `json:"votes"`
	Anchors            []string            `json:"anchors,omitempty"`
	AnchorScopes       map[string][]string `json:"anchor_scopes,omitempty"`
	Neighbors          []Neighbor          `json:"neighbors"`
	Decision           string              `json:"decision"`
	Reason             string              `json:"reason"`
	Judged             bool                `json:"judged"`
}
type Plan struct {
	DroppedAmbiguousTarget int               `json:"dropped_ambiguous_target"`
	DroppedBoundary        int               `json:"dropped_boundary"`
	DroppedInvalidTarget   int               `json:"dropped_invalid_target"`
	ClientGroups           []ClientGroup     `json:"client_groups"`
	Classes                map[string]int    `json:"class_counts"`
	Transitions            []ClassTransition `json:"class_transitions"`
	Version                int               `json:"version"`
	Created                time.Time         `json:"created"`
	Database               string            `json:"database"`
	Options                Options           `json:"options"`
	Units                  int               `json:"units"`
	Entries                []Entry           `json:"entries"`
	General                []Entry           `json:"maybe_general"`
	Scopes                 []string          `json:"scopes"`
}
type Edge struct {
	From, To int64
	Sim      float64
}
type Graph struct {
	EffectiveScopes map[int64]string
	Topics          []TopicGroup
	Units           []store.Unit
	Edges           []Edge
}

// Fingerprint makes reviewed plans fail closed after content, metadata or scope changes.
func Fingerprint(u store.Unit) string {
	raw, _ := json.Marshal(struct {
		SID, Origin, Scope, Title, Body, Category string
		Tags                                      []string
		Active                                    bool
	}{u.SID, u.Origin, u.Scope, u.Title, u.Body, u.Category, u.Tags, u.Active})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// ReadGraph opens an existing database read-only, without migrations, reindexing,
// embedder loading or native-file repair. One read transaction is a snapshot.
func ReadGraph(ctx context.Context, path string) (Graph, error) {
	var g Graph
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(30000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return g, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return g, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `WITH ranked AS (
 SELECT u.*, ROW_NUMBER() OVER(PARTITION BY u.sid ORDER BY (s.kind='native') DESC,u.updated_ns DESC,u.id) AS rn
 FROM units u JOIN sources s ON s.id=u.source_id)
 SELECT id,sid,origin,scope,title,body,category,tags,source_id,key,updated_ns FROM ranked
 WHERE rn=1 AND active=1 AND NOT EXISTS(SELECT 1 FROM suppressed s WHERE s.sid=ranked.sid) ORDER BY id`)
	if err != nil {
		return g, err
	}
	for rows.Next() {
		var u store.Unit
		var tags string
		var ns int64
		if err = rows.Scan(&u.ID, &u.SID, &u.Origin, &u.Scope, &u.Title, &u.Body, &u.Category, &tags, &u.SourceID, &u.Key, &ns); err != nil {
			rows.Close()
			return g, err
		}
		u.Active = true
		u.Updated = time.Unix(0, ns)
		u.Tags = strings.Fields(tags)
		g.Units = append(g.Units, u)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return g, err
	}
	// Graph edges can still point at upstream mirrors. Resolve those endpoints
	// to their active canonical identity, then deduplicate votes by SID in Scan.
	bySID := map[string]int64{}
	canonical := map[int64]bool{}
	for _, u := range g.Units {
		bySID[u.SID] = u.ID
		canonical[u.ID] = true
	}
	g.EffectiveScopes, err = readEffectiveScopes(ctx, tx, g.Units)
	if err != nil {
		return g, err
	}
	targets := map[int64]int64{}
	rows, err = tx.QueryContext(ctx, `SELECT id,sid FROM units WHERE active=1`)
	if err != nil {
		return g, err
	}
	for rows.Next() {
		var id int64
		var sid string
		if err = rows.Scan(&id, &sid); err != nil {
			rows.Close()
			return g, err
		}
		if target, ok := bySID[sid]; ok {
			targets[id] = target
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return g, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT unit_id,nbr_id,sim FROM neighbors ORDER BY unit_id,sim DESC,nbr_id`)
	if err != nil {
		return g, err
	}
	for rows.Next() {
		var e Edge
		if err = rows.Scan(&e.From, &e.To, &e.Sim); err != nil {
			rows.Close()
			return g, err
		}
		if target, ok := targets[e.To]; ok && canonical[e.From] {
			e.To = target
			g.Edges = append(g.Edges, e)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return g, err
	}
	g.Topics, err = readTopicGroups(ctx, tx, targets)
	if err != nil {
		return g, err
	}
	return g, tx.Commit()
}

// redacted masks credential-shaped tokens in text bound for the judge
// provider and plan files. URLs stay readable: secrets are judged, not hidden.
func redacted(s string) string { return project.MaskCredentials(s) }
func Scan(g Graph, o Options, roots map[string][]string) (Plan, error) {
	p := Plan{Version: 1, Created: time.Now().UTC(), Options: o, Units: len(g.Units), Entries: []Entry{}, General: []Entry{}}
	if err := o.Validate(); err != nil {
		return p, err
	}
	units := map[int64]store.Unit{}
	scopes := map[string]bool{}
	topScopes := map[string]bool{}
	edges := map[int64][]Edge{}
	for _, u := range g.Units {
		units[u.ID] = u
		s := g.scopeFor(u)
		topScopes[TopScope(s)] = true
		if writableScope(s) {
			scopes[s] = true
		}
	}
	for s := range scopes {
		p.Scopes = append(p.Scopes, s)
	}
	sort.Strings(p.Scopes)
	var tops []string
	for s := range topScopes {
		tops = append(tops, s)
	}
	sort.Strings(tops)
	for _, e := range g.Edges {
		if _, ok := units[e.To]; ok {
			edges[e.From] = append(edges[e.From], e)
		}
	}
	selectedGroups, groupScores := clientGroups(g, o)
	p.ClientGroups = selectedGroups
	effectiveUnits := append([]store.Unit(nil), g.Units...)
	for i := range effectiveUnits {
		effectiveUnits[i].Scope = g.scopeFor(effectiveUnits[i])
	}
	anchors := DeriveAnchors(effectiveUnits, roots)
	for _, u := range g.Units {
		own := TopScope(g.scopeFor(u))
		votes := map[string]int{}
		concreteVotes := map[string]int{}
		ns := edges[u.ID]
		sort.Slice(ns, func(i, j int) bool {
			if ns[i].Sim == ns[j].Sim {
				return ns[i].To < ns[j].To
			}
			return ns[i].Sim > ns[j].Sim
		})
		entry := Entry{SID: u.SID, Origin: u.Origin, Fingerprint: Fingerprint(u), Source: u.Scope, Text: redacted(u.Body), Votes: votes, Decision: "unsure", Reason: "unjudged proposal"}
		entry.ConcreteVotes = concreteVotes
		entry.EffectiveSource = g.scopeFor(u)
		seen := map[string]bool{u.SID: true}
		for _, e := range ns {
			v := units[e.To]
			if seen[v.SID] {
				continue
			}
			seen[v.SID] = true
			if len(entry.Neighbors) < 5 {
				entry.Neighbors = append(entry.Neighbors, Neighbor{v.SID, g.scopeFor(v), e.Sim, redacted(v.Body)})
			}
			if e.Sim >= o.MinSim {
				votes[TopScope(g.scopeFor(v))]++
				concreteVotes[g.scopeFor(v)]++
			}
		}
		voteTarget := ""
		best := 0
		tied := false
		for _, s := range tops {
			if s == own {
				continue
			}
			if votes[s] > best {
				best = votes[s]
				voteTarget = s
				tied = false
			} else if votes[s] == best && best > 0 {
				tied = true
			}
		}
		vote := !tied && best >= o.Majority && votes[own] <= o.OwnMax
		matches := MatchAnchors(u.Title+" "+u.Body, anchors)
		entry.AnchorScopes = matches
		entry.GroupScores = groupScores[u.ID]
		groupTarget := ""
		for _, s := range tops {
			if s == own || entry.GroupScores[s] < o.GroupScore || entry.GroupScores[s] < 3*entry.GroupScores[own] {
				continue
			}
			if groupTarget == "" || entry.GroupScores[s] > entry.GroupScores[groupTarget] {
				groupTarget = s
			}
		}
		target := groupTarget
		if vote && entry.GroupScores[voteTarget] >= o.GroupScore && entry.GroupScores[voteTarget] >= 3*entry.GroupScores[own] {
			target = voteTarget
		}
		if target == "" && vote {
			target = voteTarget
		}
		if target != "" {
			clientSource := strings.HasPrefix(entry.EffectiveSource, "client:")
			if (clientSource && target != own) || (!clientSource && strings.HasPrefix(target, "client:")) {
				p.DroppedBoundary++
				continue
			}
			var ambiguous bool
			entry.Target, ambiguous = concreteTarget(target, p.Scopes, concreteVotes)
			if ambiguous {
				p.DroppedAmbiguousTarget++
				continue
			}
			if entry.Target == "" {
				p.DroppedInvalidTarget++
				continue
			}
			if !targetAllowed(entry.EffectiveSource, entry.Target) {
				p.DroppedBoundary++
				continue
			}
			entry.Strength = "vote-only"
			if groupTarget != "" {
				entry.Strength = "group-only"
				if vote && target == voteTarget {
					entry.Strength = "strong"
				}
			}
			entry.Anchors = matches[target]
			entry.OwnClientGroups = topClientGroups(p.ClientGroups, own)
			entry.TargetClientGroups = topClientGroups(p.ClientGroups, target)
			entry.Reason = fmt.Sprintf("heuristic evidence: votes target=%d own=%d; groupScore target=%d own=%d; matching anchors=%s", votes[target], votes[own], entry.GroupScores[target], entry.GroupScores[own], strings.Join(entry.Anchors, ", "))
			p.Entries = append(p.Entries, entry)
		} else {
			clients, total, lo, hi := 0, 0, 17, 0
			for s, n := range votes {
				if strings.HasPrefix(s, "client:") && n >= 2 {
					clients++
					total += n
					lo = min(lo, n)
					hi = max(hi, n)
				}
			}
			if clients >= 3 && total >= o.Majority && hi-lo <= 2 {
				entry.Strength = "maybe general"
				entry.Reason = "neighbors evenly spread across clients; review global versus personal"
				p.General = append(p.General, entry)
			}
		}
	}
	sort.SliceStable(p.Entries, func(i, j int) bool {
		a, b := p.Entries[i], p.Entries[j]
		if a.Strength != b.Strength {
			return classRank(a.Strength) < classRank(b.Strength)
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.SID < b.SID
	})
	p.RefreshCounts()
	return p, nil
}

type Group struct {
	Source, Target string
	Count          int
	Decisions      map[string]int
	Examples       []Entry
}

func (p Plan) Groups() []Group {
	groups := map[string]*Group{}
	var keys []string
	for _, e := range p.Entries {
		k := effectiveSource(e) + "->" + e.Target
		if groups[k] == nil {
			groups[k] = &Group{Source: effectiveSource(e), Target: e.Target, Decisions: map[string]int{}}
			keys = append(keys, k)
		}
		g := groups[k]
		g.Count++
		g.Decisions[e.Decision]++
		if len(g.Examples) < 3 {
			g.Examples = append(g.Examples, e)
		}
	}
	sort.Strings(keys)
	out := []Group{}
	for _, k := range keys {
		out = append(out, *groups[k])
	}
	return out
}
func (p Plan) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Muninn tidy review\n\n%d active canonical units, %d suspects, %d maybe-general candidates.\n\nUnjudged proposals are unsure, never automatically approved. Review decisions and targets in the JSON before apply. Memory text is never rewritten.\n", p.Units, len(p.Entries), len(p.General))
	fmt.Fprintf(&b, "\nDropped before judging: %d provenance-boundary proposals, %d without a concrete writable target, %d ambiguous zero-vote concrete targets.\n", p.DroppedBoundary, p.DroppedInvalidTarget, p.DroppedAmbiguousTarget)
	writeSignatures(&b, p)
	fmt.Fprintf(&b, "\n## Counts per class\n\nstrong=%d group-only=%d vote-only=%d\n", p.ClassCounts()["strong"], p.ClassCounts()["group-only"], p.ClassCounts()["vote-only"])
	b.WriteString("\n## Source -> target counts per class\n")
	for _, t := range p.ClassTransitions() {
		fmt.Fprintf(&b, "\n- %s -> %s [%s]: %d\n", t.Source, t.Target, t.Class, t.Count)
	}
	for _, g := range p.Groups() {
		fmt.Fprintf(&b, "\n## %s -> %s (%d)\n\nmove=%d stay=%d unsure=%d\n", g.Source, g.Target, g.Count, g.Decisions["move"], g.Decisions["stay"], g.Decisions["unsure"])
		for _, e := range g.Examples {
			fmt.Fprintf(&b, "\n- %s [%s, %s]: %s\n  - %s\n", e.SID, e.Strength, e.Decision, strings.ReplaceAll(e.Text, "\n", " "), e.Reason)
		}
	}
	if len(p.General) > 0 {
		b.WriteString("\n## Maybe general (not move decisions)\n")
		for _, e := range p.General {
			fmt.Fprintf(&b, "\n- %s (%s): %s\n", e.SID, e.Source, strings.ReplaceAll(e.Text, "\n", " "))
		}
	}
	return b.String()
}
func Load(path string) (Plan, error) {
	var p Plan
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(b, &p)
	if err == nil && p.Version != 1 {
		err = fmt.Errorf("unsupported tidy plan version")
	}
	return p, err
}
