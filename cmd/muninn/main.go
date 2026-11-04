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
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/project"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
)

var version = "dev"

func main() {
	root := &cobra.Command{
		Use:           "muninn",
		Short:         "Local-first memory recall for AI coding agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(versionCmd(), indexCmd(), searchCmd(), recallCmd(), rememberCmd(), forgetCmd(),
		listCmd(), relatedCmd(), statsCmd(), gcCmd(), hookCmd(), serveCmd(), mcpCmd(), tuiCmd(),
		installCmd(), uninstallCmd(), configCmd(), jcodeCmd(), importCmd(), projectCmd(), syncCmd(), modelCmd())
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
		fmt.Printf("[%d] %s (%s, %s) score %.2f\n", r.Unit.ID, recall.Truncate(body, 300), r.Unit.Kind, r.Unit.Scope, r.Total)
		if explain {
			fmt.Println("    ", r.Explain())
		}
	}
}

const scopeHelp = "only these scopes (repeatable; trailing * is a prefix match, e.g. 'jcode@seid:*')"

func searchCmd() *cobra.Command {
	var limit int
	var explain, asJSON bool
	var minScore float64
	var scopes []string
	c := &cobra.Command{Use: "search <query>", Short: "Search memories (keyword + community + semantic)", Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return withApp(func(ctx context.Context, a *app.App) error {
				o := a.RecallOptions()
				o.Limit, o.MinScore = limit, minScore
				if len(scopes) > 0 {
					o.Scopes = scopes
				}
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
	c.Flags().StringSliceVar(&scopes, "scope", nil, scopeHelp)
	return c
}

func recallCmd() *cobra.Command {
	var session, harnessName string
	var stdin bool
	var scopes []string
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
				resp, err := a.Recall.Recall(ctx, recall.Request{Session: session, Harness: harnessName, Message: msg, Scopes: scopes})
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
	c.Flags().StringSliceVar(&scopes, "scope", nil, scopeHelp)
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
	var category, scope string
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
			m, reinforced, err := ingest.Remember(app.NativePath(), strings.Join(args, " "), category, scope, tags)
			if err != nil {
				return err
			}
			if reinforced {
				fmt.Printf("reinforced %s (strength %d)\n", m.ID, m.Strength)
			} else {
				fmt.Println("remembered", m.ID)
			}
			return withApp(func(ctx context.Context, a *app.App) error { _, err := a.Reindex(ctx); return err })
		}}
	c.Flags().StringVar(&category, "category", "fact", "fact | preference | correction | entity")
	c.Flags().StringVar(&scope, "scope", "global", "global or a project name")
	c.Flags().StringSliceVar(&tags, "tag", nil, "tag (repeatable)")
	c.Flags().BoolVar(&inProject, "project", false, "store in this repository's project scope (shareable with `muninn sync`)")
	return c
}

func forgetCmd() *cobra.Command {
	return &cobra.Command{Use: "forget <memory-id>", Short: "Deactivate a muninn memory", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			ok, err := ingest.Forget(app.NativePath(), args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no active muninn memory %q (harness memories are managed by their harness)", args[0])
			}
			fmt.Println("forgot", args[0])
			return withApp(func(ctx context.Context, a *app.App) error { _, err := a.Reindex(ctx); return err })
		}}
}

func listCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{Use: "list", Short: "List indexed memories", RunE: func(*cobra.Command, []string) error {
		return withApp(func(ctx context.Context, a *app.App) error {
			rows, err := a.DB.QueryContext(ctx, `SELECT id, kind, scope, body FROM units WHERE active=1 ORDER BY updated_ns DESC LIMIT ?`, limit)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id int64
				var kind, scope, body string
				rows.Scan(&id, &kind, &scope, &body)
				fmt.Printf("[%d] (%s, %s) %s\n", id, kind, scope, recall.Truncate(strings.Join(strings.Fields(body), " "), 200))
			}
			return rows.Err()
		})
	}}
	c.Flags().IntVarP(&limit, "limit", "n", 50, "max rows")
	return c
}

func relatedCmd() *cobra.Command {
	return &cobra.Command{Use: "related <unit-id>", Short: "Nearest neighbours of a memory", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var id int64
			if _, err := fmt.Sscan(args[0], &id); err != nil {
				return err
			}
			return withApp(func(ctx context.Context, a *app.App) error {
				res, err := a.Searcher.Related(ctx, id, 10)
				if err != nil {
					return err
				}
				printResults(res, false)
				return nil
			})
		}}
}

func statsCmd() *cobra.Command {
	return &cobra.Command{Use: "stats", Short: "Index statistics", RunE: func(*cobra.Command, []string) error {
		return withApp(func(ctx context.Context, a *app.App) error {
			s, err := app.Stats(ctx, a)
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(s, "", "  ")
			fmt.Println(string(b))
			return nil
		})
	}}
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
		scopes := withProject(jcodeRecallScopes(inst, a.Cfg.JcodeShareGlobal), os.Getenv("JCODE_HOOK_CWD"))
		resp, err := hookRecallWith(ctx, a, name, sid, msg, scopes)
		if err != nil || resp.Block == "" {
			logHook("jcode[%s] %s: msg=%q hits=0 skipped=%q elapsed=%s", inst.Name, short(sid), recall.Truncate(msg, 60), resp.Skipped, resp.Elapsed)
			return err
		}
		bin := inst.Binary()
		if bin == "" {
			bin = "jcode"
		}
		cmd := exec.CommandContext(ctx, bin, "transcript", "--mode", "send", "-S", sid, resp.Block)
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
	// Claude Code / Codex have no instance isolation: recall stays
	// unrestricted (nil scopes), as before. Project scoping applies where a
	// scope list exists (jcode instances).
	return hookRecallWith(ctx, a, harnessName, session, msg, withProject(nil, cwd))
}

// withProject adds the scope of the project at cwd (if any) to an explicit
// scope list. A nil list means "no restriction" and stays nil.
func withProject(scopes []string, cwd string) []string {
	if cwd == "" || scopes == nil {
		return scopes
	}
	if p, err := project.Find(cwd, false); err == nil {
		return append(scopes, p.Scope())
	}
	return scopes
}

func hookRecallWith(ctx context.Context, a *app.App, harnessName, session, msg string, scopes []string) (recall.Response, error) {
	if _, err := ensureFresh(ctx, a); err != nil {
		logHook("reindex: %v", err)
	}
	return a.Recall.Recall(ctx, recall.Request{Session: harnessName + ":" + session, Harness: harnessName, Message: msg, Scopes: scopes})
}

// jcodeRecallScopes keeps client instances isolated: a session sees its own
// instance's memories and muninn's own memories ("global" scope), never
// another instance's. shareGlobal also admits the global ~/.jcode instance.
func jcodeRecallScopes(inst harness.JcodeInstance, shareGlobal bool) []string {
	scopes := []string{inst.Scope(), inst.Scope() + ":*", "global"}
	if shareGlobal && inst.Name != "global" {
		scopes = append(scopes, "jcode@global", "jcode@global:*")
	}
	return scopes
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
