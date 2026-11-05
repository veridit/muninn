package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	Scopes       []string
	DefaultScope string
	Name         string
	Client       string
	Err          error
	writeScopes  []string
}

// Bound resolves explicit home, then JCODE_HOME, then the global instance.
// Client instances are allow-listed. Global excludes every known client,
// including indexed scopes whose instance is temporarily absent on disk.
func (a *App) Bound(jcodeHome, cwd string) Binding {
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
	if b.Err != nil {
		return nil, b.Err
	}
	units, err := a.DB.ResolveAll(ref)
	if err != nil {
		return nil, fmt.Errorf("no visible memory")
	}
	if !b.Allows(units[0].Scope) {
		return nil, fmt.Errorf("no visible memory")
	}
	var visible []store.Unit
	for _, u := range units {
		if b.Allows(u.Scope) {
			visible = append(visible, u)
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
	if b.Err != nil {
		return ForgetResult{}, b.Err
	}
	// Check the underlying write policy even for an explicitly widened view.
	units, err := a.DB.ResolveAll(ref)
	if err != nil {
		return ForgetResult{}, fmt.Errorf("no visible memory")
	}
	if !b.Allows(units[0].Scope) {
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
		if !b.Allows(u.Scope) {
			return ForgetResult{}, fmt.Errorf("no visible memory")
		}
		if _, err := b.WriteScope(u.Scope); err != nil {
			return ForgetResult{}, fmt.Errorf("no visible memory")
		}
	}
	return a.Forget(ctx, ref)
}
func (b Binding) Remember(ctx context.Context, a *App, content, category, scope string, tags []string) (store.Unit, bool, error) {
	scope, err := b.WriteScope(scope)
	if err != nil {
		return store.Unit{}, false, err
	}
	if category == "" {
		category = "fact"
	}
	m, reinforced, err := ingest.Remember(NativePath(), content, category, scope, tags)
	if err != nil {
		return store.Unit{}, false, err
	}
	if _, err := a.Reindex(ctx); err != nil {
		return store.Unit{}, false, err
	}
	u, err := b.Resolve(a, m.ID)
	return u, reinforced, err
}
