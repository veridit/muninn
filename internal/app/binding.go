package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/project"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
)

// Binding is the single visibility and write policy shared by every surface.
// Scopes supports exact names, trailing * prefixes and leading ! exclusions.
// Owner view overrides change reads only, never the underlying write policy.
type Binding struct {
	Scopes        []string
	DefaultScope  string
	Name          string
	Client        string
	Err           error
	writeScopes   []string
	sourceClients map[int64]string
	context       *bindingContext
}

type bindingContext struct {
	home, cwd string
	all       bool
	scopes    []string
}

// Bound resolves explicit home, then JCODE_HOME, then the global instance.
// Client instances are allow-listed. Global excludes every known client,
// including indexed scopes whose instance is temporarily absent on disk.
func (a *App) Bound(jcodeHome, cwd string) Binding {
	if jcodeHome == "" {
		jcodeHome = os.Getenv("JCODE_HOME")
	}
	if jcodeHome == "" {
		home, _ := os.UserHomeDir()
		jcodeHome = filepath.Join(home, ".jcode")
	}
	jcodeHome, _ = filepath.Abs(jcodeHome)
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	b := a.bound(jcodeHome, cwd)
	b.context = &bindingContext{home: jcodeHome, cwd: cwd}
	return b.Refresh(a)
}

// Refresh discovers newly indexed sources while keeping an owner's explicit
// view and the original write policy. Filesystem ancestry stays process-local.
func (b Binding) Refresh(a *App) Binding {
	if c := b.context; c != nil {
		fresh := a.bound(c.home, c.cwd).View(c.all, c.scopes)
		fresh.context = c
		b = fresh
	}
	if b.Err == nil {
		b.sourceClients, b.Err = a.sourceClients()
	}
	return b
}

func (a *App) bound(jcodeHome, cwd string) Binding {
	client, _, markerErr := project.ClientRoot(cwd)
	if markerErr != nil {
		return Binding{Name: "client (invalid marker)", Err: markerErr, Scopes: []string{"!*"}, writeScopes: []string{"!*"}}
	}
	if client != "" {
		scope := "client:" + client
		return Binding{Name: scope, Client: client, DefaultScope: scope, Scopes: []string{scope, scope + ":*"}, writeScopes: []string{scope, scope + ":*"}}
	}
	if jcodeHome == "" {
		jcodeHome = os.Getenv("JCODE_HOME")
	}
	home, _ := os.UserHomeDir()
	global := filepath.Join(home, ".jcode")
	if jcodeHome == "" {
		jcodeHome = global
	}
	jcodeHome, _ = filepath.Abs(jcodeHome)
	inst := harness.InstanceForHome(a.JcodeInstances(), jcodeHome)
	if filepath.Clean(jcodeHome) == global {
		inst.Name = "global"
	}
	b := Binding{Name: inst.Scope(), DefaultScope: inst.Scope()}
	if inst.Name != "global" {
		b.Scopes = []string{inst.Scope(), inst.Scope() + ":*", "global"}
		if cwd != "" {
			if p, err := project.Find(cwd, false); err == nil {
				b.Scopes = append(b.Scopes, p.Scope())
			}
		}
	} else {
		names := map[string]bool{}
		for _, i := range a.JcodeInstances() {
			if i.Name != "global" {
				names[i.Scope()] = true
			}
		}
		rows, err := a.DB.Query(`SELECT DISTINCT scope FROM units WHERE scope LIKE 'jcode@%'`)
		if err != nil {
			b.Scopes = []string{"!*"}
			b.Err = err
		}
		if err == nil {
			for rows.Next() {
				var s string
				if err := rows.Scan(&s); err != nil {
					b.Err = err
					break
				} else {
					s, _, _ = strings.Cut(s, ":")
					if s != "jcode@global" {
						names[s] = true
					}
				}
			}
			if err := rows.Err(); err != nil {
				b.Err = err
			}
			rows.Close()
		}
		for s := range names {
			b.Scopes = append(b.Scopes, "!"+s, "!"+s+":*")
		}
		// Project memories belong to their project: global sees only the
		// project of its working directory, like an instance binding does.
		current := ""
		if cwd != "" {
			if p, err := project.Find(cwd, false); err == nil {
				current = p.Scope()
			}
		}
		// jcode's own per-folder stores (jcode@global:<hash of the session's
		// folder>) belong to that folder too (user 2026-11-06): visible from the
		// folder itself, or from below it within the same git project. The home
		// and other parent folders are not projects, so their stores show only there.
		own := globalFolderStores(cwd)
		if grow, err := a.DB.Query(`SELECT DISTINCT scope FROM units WHERE scope LIKE 'jcode@global:%'`); err != nil {
			b.Scopes, b.Err = []string{"!*"}, err
		} else {
			for grow.Next() {
				var s string
				if grow.Scan(&s) == nil && !own[strings.TrimPrefix(s, "jcode@global:")] {
					b.Scopes = append(b.Scopes, "!"+s)
				}
			}
			if err := grow.Err(); err != nil {
				b.Err = err
			}
			grow.Close()
		}
		// Imports from folders that are not projects keep their folder as the
		// scope, folder:<path> (user 2026-11-07). Same rule as the stores above.
		if frow, err := a.DB.Query(`SELECT DISTINCT scope FROM units WHERE scope LIKE 'folder:%'`); err != nil {
			b.Scopes, b.Err = []string{"!*"}, err
		} else {
			for frow.Next() {
				var s string
				if frow.Scan(&s) == nil && !FolderOwns(strings.TrimPrefix(s, "folder:"), cwd) {
					b.Scopes = append(b.Scopes, "!"+s)
				}
			}
			if err := frow.Err(); err != nil {
				b.Err = err
			}
			frow.Close()
		}
		if prow, err := a.DB.Query(`SELECT DISTINCT scope FROM units WHERE scope LIKE 'project:%'`); err != nil {
			b.Scopes, b.Err = []string{"!*"}, err
		} else {
			for prow.Next() {
				var s string
				if prow.Scan(&s) == nil && s != current {
					b.Scopes = append(b.Scopes, "!"+s)
				}
			}
			if err := prow.Err(); err != nil {
				b.Err = err
			}
			prow.Close()
		}
		sort.Strings(b.Scopes)
	}
	b.Scopes = append(b.Scopes, "!client:*")
	b.writeScopes = append([]string{}, b.Scopes...)
	return b
}

// View explicitly widens an owner view. It never widens writes.
func (b Binding) View(all bool, scopes []string) Binding {
	if b.Client != "" || b.Err != nil {
		return b
	}
	if b.context != nil {
		c := *b.context
		c.all = all
		c.scopes = append([]string(nil), scopes...)
		b.context = &c
	}
	if all {
		b.Scopes = nil
		b.Name += " (all)"
	}
	if len(scopes) > 0 {
		b.Scopes = append([]string(nil), scopes...)
		b.Name += " (scope: " + strings.Join(scopes, ", ") + ")"
	}
	// Owner view flags may cross instance views, never a client boundary.
	b.Scopes = append(b.Scopes, "!client:*")
	return b
}
func (b Binding) Allows(scope string) bool { return b.Err == nil && search.Allowed(scope, b.Scopes) }
func (b Binding) WriteScope(scope string) (string, error) {
	if b.Err != nil {
		return "", b.Err
	}
	if scope == "" {
		scope = b.DefaultScope
	}
	if scope == "" {
		scope = "global"
	}
	if strings.HasPrefix(scope, "jcode@") && b.DefaultScope != "" {
		instance, _, _ := strings.Cut(scope, ":")
		own, _, _ := strings.Cut(b.DefaultScope, ":")
		if instance != own {
			return "", fmt.Errorf("scope %q is not available in this binding", scope)
		}
	}
	rules := b.writeScopes
	if rules == nil {
		rules = b.Scopes
	}
	if !search.Allowed(scope, rules) {
		return "", fmt.Errorf("scope %q is not available in this binding", scope)
	}
	return scope, nil
}

func (b Binding) ResolveAll(a *App, ref string) ([]store.Unit, error) {
	b = b.Refresh(a)
	if b.Err != nil {
		return nil, b.Err
	}
	units, err := a.DB.ResolveAll(ref)
	if err != nil {
		return nil, fmt.Errorf("no visible memory")
	}
	if _, ok := b.Present(units[0]); !ok {
		return nil, fmt.Errorf("no visible memory")
	}
	var visible []store.Unit
	for _, u := range units {
		if shown, ok := b.Present(u); ok {
			visible = append(visible, shown)
		}
	}
	if len(visible) == 0 {
		return nil, fmt.Errorf("no visible memory")
	}
	return visible, nil
}
func (b Binding) Resolve(a *App, ref string) (store.Unit, error) {
	units, err := b.ResolveAll(a, ref)
	if err != nil {
		return store.Unit{}, err
	}
	return units[0], nil
}
func (b Binding) Forget(ctx context.Context, a *App, ref string) (ForgetResult, error) {
	b = b.Refresh(a)
	if b.Err != nil {
		return ForgetResult{}, b.Err
	}
	// Check the underlying write policy even for an explicitly widened view.
	units, err := a.DB.ResolveAll(ref)
	if err != nil {
		return ForgetResult{}, fmt.Errorf("no visible memory")
	}
	if _, ok := b.Present(units[0]); !ok {
		return ForgetResult{}, fmt.Errorf("no visible memory")
	}
	for _, u := range units {
		var kind string
		if err := a.DB.QueryRow(`SELECT kind FROM sources WHERE id=?`, u.SourceID).Scan(&kind); err != nil {
			return ForgetResult{}, err
		}
		// The canonical memory owns the identity. Suppressing an upstream
		// mirror is safe after an explicit rescope, even in its old scope.
		if kind != "native" && u.ID != units[0].ID {
			continue
		}
		shown, ok := b.Present(u)
		if !ok {
			return ForgetResult{}, fmt.Errorf("no visible memory")
		}
		if _, err := b.WriteScope(shown.Scope); err != nil {
			return ForgetResult{}, fmt.Errorf("no visible memory")
		}
	}
	return a.Forget(ctx, ref)
}

type sourceBoundary struct {
	client string
	err    error
}

// Cache filesystem ancestry only in this process, never in the index. Native
// storage pools many scopes, so only non-native paths carry source provenance.
func (a *App) sourceClients() (map[int64]string, error) {
	rows, err := a.DB.Query(`SELECT id,path,kind FROM sources`)
	if err != nil {
		return nil, err
	}
	type source struct {
		id         int64
		path, kind string
	}
	var sources []source
	for rows.Next() {
		var s source
		if err := rows.Scan(&s.id, &s.path, &s.kind); err != nil {
			rows.Close()
			return nil, err
		}
		sources = append(sources, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	a.sourceMu.Lock()
	defer a.sourceMu.Unlock()
	if a.sourceRoots == nil {
		a.sourceRoots = map[string]sourceBoundary{}
	}
	out := map[int64]string{}
	for _, s := range sources {
		if s.kind == "native" {
			out[s.id] = "!native"
			continue
		}
		root, ok := a.sourceRoots[s.path]
		if !ok {
			root.client, root.err = project.SourceClient(s.path)
			a.sourceRoots[s.path] = root
		}
		if root.err != nil {
			out[s.id] = "!invalid"
		} else {
			out[s.id] = root.client
		}
	}
	return out, nil
}

// ScopeFor overlays a physical client's scope without changing stored ids,
// scope or origin. Unmarked non-native sources have no client provenance.
func (b Binding) ScopeFor(u store.Unit) (string, bool) {
	if b.Err != nil {
		return "", false
	}
	if b.sourceClients != nil {
		client, known := b.sourceClients[u.SourceID]
		if !known {
			return "", false
		}
		if client == "!native" {
			return u.Scope, true
		}
		if client == "!invalid" || client != b.Client {
			return "", false
		}
		if client != "" {
			return "client:" + client, true
		}
	}
	return u.Scope, true
}
func (b Binding) Present(u store.Unit) (store.Unit, bool) {
	scope, ok := b.ScopeFor(u)
	u.Scope = scope
	return u, ok && b.Allows(scope)
}

// ScopeExpr and SQL are the SQL equivalent of ScopeFor + Allows, used by
// list, stats and injection history so no surface maintains a second policy.
func (b Binding) ScopeExpr(scopeCol, sourceCol string) string {
	var ids []int64
	for id, c := range b.sourceClients {
		if c != "" && c != "!invalid" && c != "!native" {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) == 0 {
		return scopeCol
	}
	var out strings.Builder
	out.WriteString("CASE " + sourceCol)
	for _, id := range ids {
		fmt.Fprintf(&out, " WHEN %d THEN 'client:%s'", id, b.sourceClients[id])
	}
	out.WriteString(" ELSE " + scopeCol + " END")
	return out.String()
}
func (b Binding) SQL(scopeCol, sourceCol string) (string, []any) {
	if b.Err != nil {
		return "0", nil
	}
	where, args := search.ScopeSQL(b.ScopeExpr(scopeCol, sourceCol), b.Scopes)
	// A concurrent indexer may register a source after this snapshot. Unknown
	// provenance is invisible until the next refresh, just like ScopeFor.
	if b.sourceClients != nil {
		var known []string
		for id := range b.sourceClients {
			known = append(known, strconv.FormatInt(id, 10))
		}
		if len(known) == 0 {
			return "0", nil
		}
		sort.Strings(known)
		where = "(" + where + ") AND " + sourceCol + " IN (" + strings.Join(known, ",") + ")"
	}
	var hidden []int64
	for id, c := range b.sourceClients {
		if c != "!native" && (c == "!invalid" || c != b.Client) {
			hidden = append(hidden, id)
		}
	}
	sort.Slice(hidden, func(i, j int) bool { return hidden[i] < hidden[j] })
	if len(hidden) > 0 {
		var ids []string
		for _, id := range hidden {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		where = "(" + where + ") AND " + sourceCol + " NOT IN (" + strings.Join(ids, ",") + ")"
	}
	return where, args
}
func (b Binding) Remember(ctx context.Context, a *App, content, category, scope string, tags []string) (store.Unit, bool, error) {
	return b.RememberSafe(ctx, a, content, category, scope, tags, "")
}

// SecretCheckError asks the writer to judge text that looked secret. It is a
// question, not a verdict: storing again with a safe reason proceeds.
type SecretCheckError struct{ Hint string }

func (e *SecretCheckError) Error() string {
	return "not stored yet: this looks like it may contain " + e.Hint + ". Are you sure there are no secrets here? " +
		"If it is safe (for example a browser key that ships in public pages, a secrets.NAME reference, a placeholder, or a URL meant to be shared), " +
		"store it again with a reason saying why (CLI --safe-reason, MCP safe_reason). Otherwise remove the secret and store the rest."
}

// RememberSafe stores a memory. When the cheap secret check fires, the write
// needs safeReason: the writer's judgment that the text is safe to keep.
func (b Binding) RememberSafe(ctx context.Context, a *App, content, category, scope string, tags []string, safeReason string) (store.Unit, bool, error) {
	b = b.Refresh(a)
	safeReason = strings.TrimSpace(safeReason)
	if hint := project.SecretHint(content); hint != "" && safeReason == "" {
		return store.Unit{}, false, &SecretCheckError{Hint: hint}
	} else if hint == "" {
		safeReason = ""
	}
	scope, err := b.WriteScope(scope)
	if err != nil {
		return store.Unit{}, false, err
	}
	if category == "" {
		category = "fact"
	}
	m, reinforced, err := ingest.RememberSafe(NativePath(), content, category, scope, tags, safeReason)
	if err != nil {
		return store.Unit{}, false, err
	}
	if _, err := a.Reindex(ctx); err != nil {
		return store.Unit{}, false, err
	}
	u, err := b.Resolve(a, m.ID)
	return u, reinforced, err
}

// globalFolderStores names the jcode@global:<hash> stores that belong to a
// session in cwd: the folder itself, and inside a git project every folder up
// to its top level, so a subfolder session still sees the project root's
// store. (Hashes cannot be inverted, so a subfolder's store shows only from
// that subfolder down; tidy can move it to the project.) Outside git only the
// exact folder counts, so a home or parent-folder store never leaks into
// child folders.
// Paths are tried as given, with symlinks resolved, and in the literal "~/"
// form some jcode sessions recorded.
func globalFolderStores(cwd string) map[string]bool {
	own := map[string]bool{}
	if cwd == "" {
		return own
	}
	home, _ := os.UserHomeDir()
	add := func(dir string) {
		own[harness.JcodeProjectHash(dir)] = true
		if home != "" {
			if rel, err := filepath.Rel(home, dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				own[harness.JcodeProjectHash("~/"+rel)] = true
			}
		}
	}
	dirs := []string{filepath.Clean(cwd)}
	if phys, err := filepath.EvalSymlinks(cwd); err == nil && phys != dirs[0] {
		dirs = append(dirs, phys)
	}
	top := ""
	if p, err := project.Find(cwd, false); err == nil && p.Root != "" {
		top = filepath.Clean(p.Root)
	}
	for _, d := range dirs {
		add(d)
		if top == "" {
			continue
		}
		for anc := d; ; anc = filepath.Dir(anc) {
			add(anc)
			if anc == top || filepath.Dir(anc) == anc {
				break
			}
			if rel, err := filepath.Rel(top, anc); err != nil || strings.HasPrefix(rel, "..") {
				break
			}
		}
	}
	return own
}

// FolderOwns reports whether a session in cwd sees a folder:<dir> memory: the
// session is in dir itself, or below it. The home folder and workspace folders
// (two or more git repositories directly inside, such as ~/Projects) are parents,
// not projects, so their memories show only in exactly that folder.
func FolderOwns(dir, cwd string) bool {
	if dir == "" || cwd == "" {
		return false
	}
	dir = filepath.Clean(dir)
	cwds := []string{filepath.Clean(cwd)}
	if phys, err := filepath.EvalSymlinks(cwd); err == nil && phys != cwds[0] {
		cwds = append(cwds, phys)
	}
	for _, c := range cwds {
		if c == dir {
			return true
		}
	}
	if isParentFolder(dir) {
		return false
	}
	for _, c := range cwds {
		if strings.HasPrefix(c, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func isParentFolder(dir string) bool {
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == dir {
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	repos := 0
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), ".git")); err == nil {
				if repos++; repos >= 2 {
					return true
				}
			}
		}
	}
	return false
}
