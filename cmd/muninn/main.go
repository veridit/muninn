package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/project"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
)

type viewFlags struct {
	all    bool
	scopes []string
	home   string
}

func (v *viewFlags) flags(c *cobra.Command) {
	c.Flags().BoolVar(&v.all, "all", false, "owner view: read all instances (writes remain bound)")
	c.Flags().StringSliceVar(&v.scopes, "scope", nil, scopeHelp)
	c.Flags().StringVar(&v.home, "jcode-home", "", "bind to this JCODE_HOME (default $JCODE_HOME, otherwise global)")
	c.MarkFlagsMutuallyExclusive("all", "scope")
}
func (v viewFlags) binding(a *app.App) app.Binding {
	cwd, _ := os.Getwd()
	return a.Bound(v.home, cwd).View(v.all, v.scopes)
}

var version = "dev"

func main() {
	root := &cobra.Command{
		Use:           "muninn",
		Short:         "Local-first memory recall for AI coding agents",
		Example:       "  muninn search \"banana mode\"\n  muninn forget k7m2qx\n  muninn remember \"The corrected fact\"",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(versionCmd(), indexCmd(), searchCmd(), recallCmd(), rememberCmd(), forgetCmd(), showCmd(), rescopeCmd(),
		listCmd(), relatedCmd(), statsCmd(), gcCmd(), hookCmd(), serveCmd(), mcpCmd(), tuiCmd(),
		installCmd(), uninstallCmd(), configCmd(), jcodeCmd(), importCmd(), projectCmd(), syncCmd(), modelCmd(), captureCmd())
	if p := os.Getenv("MUNINN_CPUPROFILE"); p != "" {
		if f, err := os.Create(p); err == nil {
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}
	}
	if err := root.Execute(); err != nil {
		pprof.StopCPUProfile()
		fmt.Fprintln(os.Stderr, "muninn:", err)
		os.Exit(1)
	}
}

func withApp(fn func(ctx context.Context, a *app.App) error) error {
	ctx := context.Background()
	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	cwd, _ := os.Getwd()
	if b := a.Bound("", cwd); b.Err != nil {
		return b.Err
	}
	return fn(ctx, a)
}

func versionCmd() *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print version", Run: func(*cobra.Command, []string) {
		fmt.Println("muninn", version)
	}}
}

func indexCmd() *cobra.Command {
	return &cobra.Command{Use: "index", Short: "Incrementally index all configured sources", RunE: func(*cobra.Command, []string) error {
		return withApp(func(ctx context.Context, a *app.App) error {
			fmt.Fprintln(os.Stderr, "embedder:", a.EmbedNote)
			st, err := a.Reindex(ctx)
			if err != nil {
				return err
			}
			fmt.Println(st)
			if st.EmbedErr != nil {
				fmt.Fprintln(os.Stderr, "embedding skipped:", st.EmbedErr)
			}
			return nil
		})
	}}
}

func printResults(res []search.Result, explain bool) {
	if len(res) == 0 {
		fmt.Println("no results")
		return
	}
	for _, r := range res {
		body := strings.Join(strings.Fields(r.Unit.Body), " ")
		fmt.Printf("[%s] %s (%s, %s) score %.2f\n", r.Unit.SID, recall.Truncate(body, 300), r.Unit.Kind, r.Unit.Scope, r.Total)
		if explain {
			fmt.Println("    ", r.Explain(), "origin:", r.Unit.Origin)
		}
	}
}

const scopeHelp = "only these scopes (repeatable; trailing * is a prefix match, e.g. 'jcode@seid:*')"

func searchCmd() *cobra.Command {
	var limit int
	var explain, asJSON bool
	var minScore float64
	var view viewFlags
	c := &cobra.Command{Use: "search <query>", Short: "Search memories (keyword + community + semantic)", Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return withApp(func(ctx context.Context, a *app.App) error {
				o := a.RecallOptions()
				o.Limit, o.MinScore = limit, minScore
				b := view.binding(a)
				o.Scopes, o.ScopeFor = b.Scopes, b.ScopeFor
				res, err := a.Searcher.Search(ctx, strings.Join(args, " "), o)
				if err != nil {
					return err
				}
				if asJSON {
					return json.NewEncoder(os.Stdout).Encode(res)
				}
				printResults(res, explain)
				return nil
			})
		}}
	c.Flags().IntVarP(&limit, "limit", "n", 10, "max results")
	c.Flags().BoolVar(&explain, "explain", false, "show per-signal scores")
	c.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	c.Flags().Float64Var(&minScore, "min-score", 0, "drop results below this total")
	view.flags(c)
	return c
}

func recallCmd() *cobra.Command {
	var session, harnessName string
	var stdin bool
	var view viewFlags
	c := &cobra.Command{Use: "recall [message]", Short: "Automatic-recall block for a message (generic harness entry point)",
		RunE: func(_ *cobra.Command, args []string) error {
			msg := strings.Join(args, " ")
			if stdin {
				b, _ := ioReadAll(os.Stdin)
				msg = string(b)
			}
			return withApp(func(ctx context.Context, a *app.App) error {
				if _, err := ensureFresh(ctx, a); err != nil {
					return err
				}
				binding := view.binding(a)
				resp, err := a.Recall.Recall(ctx, recall.Request{Session: session, Harness: harnessName, Message: msg, Scopes: binding.Scopes, ScopeFor: binding.ScopeFor})
				if err != nil {
					return err
				}
				if resp.Block != "" {
					fmt.Println(resp.Block)
				}
				return nil
			})
		}}
	c.Flags().StringVar(&session, "session", "", "session id for per-session dedupe")
	c.Flags().StringVar(&harnessName, "harness", "cli", "harness name for the injection log")
	c.Flags().BoolVar(&stdin, "stdin", false, "read the message from stdin")
	view.flags(c)
	return c
}

// ensureFresh runs a cheap incremental pass (stat-only when nothing changed).
func ensureFresh(ctx context.Context, a *app.App) (index.Stats, error) {
	// Recall paths must stay fast: never embed inline beyond a handful of
	// brand-new memories (one Ollama batch is ~0.2-2 s). Larger backlogs,
	// including a model or prompt change, are left to `muninn index`/`serve`.
	a.Indexer.MaxEmbed = 8
	// If another process is indexing, do not queue behind it: WAL still lets
	// this process read a consistent snapshot, so recall just runs on that.
	a.Indexer.NoWait = true
	defer func() { a.Indexer.MaxEmbed, a.Indexer.NoWait = 0, false }()
	st, err := a.Reindex(ctx)
	if errors.Is(err, index.ErrBusy) {
		return st, nil
	}
	return st, err
}

func rememberCmd() *cobra.Command {
	var category, scope, home string
	var tags []string
	var inProject bool
	c := &cobra.Command{Use: "remember <text>", Short: "Store a memory", Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if inProject {
				p, err := project.Find(dirOr(""), true)
				if err != nil {
					return err
				}
				scope = p.Scope()
			}
			return withApp(func(ctx context.Context, a *app.App) error {
				cwd, _ := os.Getwd()
				u, reinforced, err := a.Bound(home, cwd).Remember(ctx, a, strings.Join(args, " "), category, scope, tags)
				if err != nil {
					return err
				}
				if reinforced {
					fmt.Printf("reinforced %s\n", u.SID)
				} else {
					fmt.Println("remembered", u.SID)
				}
				return nil
			})
		}}
	c.Flags().StringVar(&category, "category", "fact", "fact | preference | correction | entity")
	c.Flags().StringVar(&scope, "scope", "", "memory scope (default: bound instance)")
	c.Flags().StringVar(&home, "jcode-home", "", "bind to this JCODE_HOME")
	c.Flags().StringSliceVar(&tags, "tag", nil, "tag (repeatable)")
	c.Flags().BoolVar(&inProject, "project", false, "store in this repository's project scope (shareable with `muninn sync`)")
	return c
}

func forgetCmd() *cobra.Command {
	var view viewFlags
	c := &cobra.Command{Use: "forget <memory-id>", Short: "Retire a memory or suppress an upstream memory locally", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		return withApp(func(ctx context.Context, a *app.App) error {
			if _, err := a.Reindex(ctx); err != nil {
				return err
			}
			out, err := view.binding(a).Forget(ctx, a, args[0])
			if err != nil {
				return err
			}
			fmt.Println(out.Message)
			return nil
		})
	}}
	view.flags(c)
	return c
}

func rescopeCmd() *cobra.Command {
	var target string
	var cross bool
	c := &cobra.Command{Use: "rescope <memory-id>", Short: "Admin: move an owned memory to another scope", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		return withApp(func(ctx context.Context, a *app.App) error {
			cwd, _ := os.Getwd()
			id, err := a.Bound("", cwd).Rescope(ctx, a, args[0], target, cross)
			if err != nil {
				return err
			}
			fmt.Printf("rescoped %s to %s\n", id, target)
			return nil
		})
	}}
	c.Flags().StringVar(&target, "to", "", "literal target scope")
	c.Flags().BoolVar(&cross, "cross-client", false, "admin approval to cross a client boundary")
	c.MarkFlagRequired("to")
	return c
}

func showCmd() *cobra.Command {
	var view viewFlags
	c := &cobra.Command{Use: "show <memory-id>", Short: "Show memory text and its upstream mapping", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		return withApp(func(ctx context.Context, a *app.App) error {
			units, err := view.binding(a).ResolveAll(a, args[0])
			if err != nil {
				return err
			}
			canonical := units[0]
			fmt.Printf("[%s] %s\ncategory: %s\n", canonical.SID, canonical.Body, canonical.Category)
			for _, u := range units {
				o, err := a.Origin(u)
				if err != nil {
					return err
				}
				fmt.Printf("source: %s\npath: %s\nkey: %s\norigin: %s\n", o.Source, o.Path, o.Key, o.Origin)
				if u.ID != canonical.ID && u.ContentHash != canonical.ContentHash {
					fmt.Printf("mirror text: %s\n", u.Body)
				}
			}
			return nil
		})
	}}
	view.flags(c)
	return c
}

func listCmd() *cobra.Command {
	var limit int
	var view viewFlags
	c := &cobra.Command{Use: "list", Short: "List indexed memories", RunE: func(*cobra.Command, []string) error {
		return withApp(func(ctx context.Context, a *app.App) error {
			b := view.binding(a)
			where, params := b.SQL("u.scope", "u.source_id")
			rows, err := a.DB.QueryContext(ctx, `SELECT sid, kind, `+b.ScopeExpr("u.scope", "u.source_id")+`, category, body FROM units u WHERE (`+where+`) AND active=1 AND sid NOT IN (SELECT sid FROM suppressed)
    AND u.id=(SELECT c.id FROM units c JOIN sources cs ON cs.id=c.source_id WHERE c.sid=u.sid ORDER BY (cs.kind='native') DESC,c.updated_ns DESC,c.id LIMIT 1)
    ORDER BY updated_ns DESC LIMIT ?`, append(params, limit)...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				var kind, scope, category, body string
				if err := rows.Scan(&id, &kind, &scope, &category, &body); err != nil {
					return err
				}
				fmt.Printf("[%s] (%s, %s, %s) %s\n", id, kind, scope, category, recall.Truncate(strings.Join(strings.Fields(body), " "), 200))
			}
			return rows.Err()
		})
	}}
	c.Flags().IntVarP(&limit, "limit", "n", 50, "max rows")
	view.flags(c)
	return c
}

func relatedCmd() *cobra.Command {
	var view viewFlags
	c := &cobra.Command{Use: "related <unit-id>", Short: "Nearest neighbours of a memory", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return withApp(func(ctx context.Context, a *app.App) error {
				binding := view.binding(a)
				u, err := binding.Resolve(a, args[0])
				if err != nil {
					return err
				}
				res, err := a.Searcher.RelatedIn(ctx, u.ID, 10, binding.Scopes, binding.ScopeFor)
				if err != nil {
					return err
				}
				printResults(res, false)
				return nil
			})
		}}
	view.flags(c)
	return c
}

func statsCmd() *cobra.Command {
	var view viewFlags
	c := &cobra.Command{Use: "stats", Short: "Index statistics", RunE: func(*cobra.Command, []string) error {
		return withApp(func(ctx context.Context, a *app.App) error {
			s, err := app.Stats(ctx, a, view.binding(a))
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(s, "", "  ")
			fmt.Println(string(b))
			return nil
		})
	}}
	view.flags(c)
	return c
}

func gcCmd() *cobra.Command {
	return &cobra.Command{Use: "gc", Short: "Drop vectors from old pipelines or deleted units", RunE: func(*cobra.Command, []string) error {
		return withApp(func(ctx context.Context, a *app.App) error {
			if a.Embedder == nil {
				return fmt.Errorf("embedder unavailable; refusing to gc without knowing the current pipeline")
			}
			n, err := a.DB.GC(index.PipelineHash(a.Embedder.Model()))
			fmt.Println("removed", n, "vectors")
			return err
		})
	}}
}

// hookCmd is the harness entry point: `muninn hook claude|codex|jcode`.
func hookCmd() *cobra.Command {
	var budget time.Duration
	c := &cobra.Command{Use: "hook <claude|codex|jcode>", Short: "Run as a harness hook (reads the harness payload)", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			// A hook must never break the harness: errors are logged, exit 0.
			if err := runHook(args[0], budget); err != nil {
				logHook("%s: %v", args[0], err)
			}
			return nil
		}}
	c.Flags().DurationVar(&budget, "budget", 8*time.Second, "time budget for recall")
	return c
}

func runHook(name string, budget time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	switch name {
	case "claude", "codex":
		in, err := harness.ReadPromptHook(os.Stdin)
		if err != nil {
			return err
		}
		resp, err := hookRecall(ctx, name, in.SessionID, in.Prompt, in.Cwd)
		if err != nil {
			return err
		}
		os.Stdout.Write(harness.PromptHookOutput(resp.Block))
		return nil
	case "jcode":
		sid := os.Getenv("JCODE_HOOK_SESSION_ID")
		if sid == "" {
			return nil
		}
		// Each jcode instance runs with its own JCODE_HOME; the hook inherits
		// it. Use that instance's sessions, binary and memories only.
		jh := os.Getenv("JCODE_HOME")
		if jh == "" {
			home, _ := os.UserHomeDir()
			jh = filepath.Join(home, ".jcode")
		}
		msg, err := harness.JcodeLastUserMessage(filepath.Join(jh, "sessions"), sid)
		if err != nil {
			return err
		}
		a, err := app.Open(ctx)
		if err != nil {
			return err
		}
		defer a.Close()
		inst := harness.InstanceForHome(a.JcodeInstances(), jh)
		binding := a.Bound(jh, os.Getenv("JCODE_HOOK_CWD"))
		if binding.Err != nil {
			return binding.Err
		}
		resp, err := hookRecallWith(ctx, a, name, sid, msg, binding)
		if err != nil || resp.Block == "" {
			logHook("jcode[%s] %s: msg=%q hits=0 skipped=%q elapsed=%s", inst.Name, short(sid), recall.Truncate(msg, 60), resp.Skipped, resp.Elapsed)
			return err
		}
		bin := inst.Binary()
		if bin == "" {
			bin = "jcode"
		}
		// The block goes on stdin, never argv: it starts with "-- muninn recall",
		// which jcode's argument parser rejects as an unknown flag (exit 2).
		cmd := exec.CommandContext(ctx, bin, "transcript", "--mode", "send", "-S", sid)
		cmd.Stdin = strings.NewReader(resp.Block)
		cmd.Env = append(os.Environ(), "JCODE_HOME="+inst.Home)
		if os.Getenv("JCODE_RUNTIME_DIR") == "" {
			if rt := filepath.Join(inst.Home, "runtime"); dirExists(rt) {
				cmd.Env = append(cmd.Env, "JCODE_RUNTIME_DIR="+rt)
			}
		}
		out, err := cmd.CombinedOutput()
		logHook("jcode[%s] %s: msg=%q hits=%d elapsed=%s transcript_err=%v %s", inst.Name, short(sid), recall.Truncate(msg, 60),
			len(resp.Hits), resp.Elapsed, err, strings.TrimSpace(string(out)))
		return err
	default:
		return fmt.Errorf("unknown harness %q (claude, codex, jcode)", name)
	}
}

func hookRecall(ctx context.Context, harnessName, session, msg, cwd string) (recall.Response, error) {
	a, err := app.Open(ctx)
	if err != nil {
		return recall.Response{}, err
	}
	defer a.Close()
	b := a.Bound("", cwd)
	if b.Err != nil {
		return recall.Response{}, b.Err
	}
	return hookRecallWith(ctx, a, harnessName, session, msg, b)
}

func hookRecallWith(ctx context.Context, a *app.App, harnessName, session, msg string, b app.Binding) (recall.Response, error) {
	if _, err := ensureFresh(ctx, a); err != nil {
		logHook("reindex: %v", err)
	}
	b = b.Refresh(a)
	return a.Recall.Recall(ctx, recall.Request{Session: harnessName + ":" + session, Harness: harnessName, Message: msg, Scopes: b.Scopes, ScopeFor: b.ScopeFor})
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func short(s string) string {
	if len(s) > 24 {
		return s[:24]
	}
	return s
}

func logHook(format string, args ...any) {
	p := filepath.Join(config.DataDir(), "hook.log")
	os.MkdirAll(filepath.Dir(p), 0o700)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s "+format+"\n", append([]any{time.Now().Format(time.RFC3339)}, args...)...)
}

func configCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Show paths and effective configuration", RunE: func(*cobra.Command, []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		fmt.Println("config:", filepath.Join(config.ConfigDir(), "config.json"))
		fmt.Println("data:  ", config.DataDir())
		b, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Println(string(b))
		return nil
	}}
	c.AddCommand(&cobra.Command{Use: "init", Short: "Write a config.json with defaults", RunE: func(*cobra.Command, []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return config.Save(cfg)
	}})
	return c
}
