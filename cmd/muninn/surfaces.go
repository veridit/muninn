package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/daemon"
	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/mcpserver"
	"github.com/veridit/muninn/internal/tui"
	"github.com/veridit/muninn/internal/web"
)

func ioReadAll(r io.Reader) ([]byte, error) { return io.ReadAll(io.LimitReader(r, 4<<20)) }

func serveCmd() *cobra.Command {
	var addr string
	var noWatch bool
	var view viewFlags
	c := &cobra.Command{Use: "serve", Short: "Run the watcher (incremental reindex on change) and the local web UI",
		RunE: func(*cobra.Command, []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			// serve is the process that holds the built-in model for hooks;
			// it must load it itself, never ask another serve.
			os.Setenv("MUNINN_SERVE", "1")
			return withApp(func(_ context.Context, a *app.App) error {
				if addr == "" {
					addr = a.Cfg.ListenAddr
				}
				fmt.Println("embedder:", a.EmbedNote)
				mu := &sync.Mutex{}
				if !noWatch {
					go func() {
						if err := daemon.Watch(ctx, a, mu, daemon.StdLog); err != nil {
							daemon.StdLog("watcher stopped: %v", err)
						}
					}()
				}
				fmt.Printf("muninn web UI on http://%s\n", addr)
				return web.ListenAndServe(ctx, addr, (&web.Server{App: a, Mu: mu, Binding: view.binding(a)}).Handler())
			})
		}}
	c.Flags().StringVar(&addr, "addr", "", "loopback listen address (default from config, 127.0.0.1:7431)")
	c.Flags().BoolVar(&noWatch, "no-watch", false, "do not watch sources")
	view.flags(c)
	return c
}

func mcpCmd() *cobra.Command {
	var jcodeHome string
	c := &cobra.Command{Use: "mcp", Short: "Serve MCP over stdio (memory_search, memory_remember, ...)",
		RunE: func(*cobra.Command, []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return withApp(func(_ context.Context, a *app.App) error {
				if _, err := a.Reindex(ctx); err != nil {
					fmt.Fprintln(os.Stderr, "muninn: reindex:", err)
				}
				cwd, _ := os.Getwd()
				return mcpserver.RunStdio(ctx, a, version, a.Bound(jcodeHome, cwd))
			})
		}}
	c.Flags().StringVar(&jcodeHome, "jcode-home", "", "serve one jcode instance: same scopes as its recall hook (default $JCODE_HOME when set)")
	return c
}

func tuiCmd() *cobra.Command {
	var view viewFlags
	c := &cobra.Command{Use: "tui", Short: "Interactive terminal browser", RunE: func(*cobra.Command, []string) error {
		return withApp(func(ctx context.Context, a *app.App) error {
			if _, err := a.Reindex(ctx); err != nil {
				return err
			}
			return tui.Run(a, view.binding(a))
		})
	}}
	view.flags(c)
	return c
}

func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		return "muninn"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

var installers = map[string]func(bin string, install, dry bool) (harness.InstallResult, error){
	"claude": harness.InstallClaude,
	"codex":  harness.InstallCodex,
	"jcode":  harness.InstallJcode,
}

func installLike(install bool) *cobra.Command {
	var dry, allInstances bool
	var bin string
	var homes []string
	use, short := "install <claude|codex|jcode|all>", "Wire automatic recall into a harness (keeps a backup)"
	if !install {
		use, short = "uninstall <claude|codex|jcode|all>", "Remove muninn's hook from a harness"
	}
	c := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if bin == "" {
			bin = selfPath()
		}
		names := []string{args[0]}
		if args[0] == "all" {
			names = []string{"claude", "codex", "jcode"}
		}
		for _, n := range names {
			if n == "jcode" && (allInstances || len(homes) > 0) {
				if err := installJcodeInstances(bin, homes, allInstances, install, dry); err != nil {
					return err
				}
				continue
			}
			f, ok := installers[n]
			if !ok {
				return fmt.Errorf("unknown harness %q", n)
			}
			r, err := f(bin, install, dry)
			if err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
			if dry {
				fmt.Print("[dry-run] ")
			}
			fmt.Println(r)
		}
		// One command should leave a working setup: fetch the built-in
		// embedding model (pinned, verified) unless Ollama already serves one.
		if install && !dry && !embed.Builtin.Present(config.DataDir()) {
			cfg, _ := config.Load()
			o := embed.NewOllama(cfg.OllamaURL, cfg.EmbedModel, 0)
			if cfg.Embedder == "builtin" || (cfg.Embedder == "auto" && !o.Available(context.Background())) {
				fmt.Fprintln(os.Stderr, "fetching the built-in embedding model (~127 MB, once; pinned and sha256-verified)")
				if err := embed.Builtin.Download(context.Background(), config.DataDir(), nil); err != nil {
					fmt.Fprintln(os.Stderr, "model download failed (muninn works keyword-only until `muninn model download` succeeds):", err)
				}
			}
		}
		return nil
	}}
	c.Flags().BoolVar(&dry, "dry-run", false, "show what would change")
	c.Flags().StringVar(&bin, "bin", "", "muninn binary path to put in the hook (default: this binary)")
	c.Flags().BoolVar(&allInstances, "all-instances", false, "jcode: every instance (~/.jcode, ~/.zshrc JCODE_INSTANCES, config jcode_homes)")
	c.Flags().StringSliceVar(&homes, "jcode-home", nil, "jcode: a specific JCODE_HOME (repeatable)")
	return c
}

func installJcodeInstances(bin string, homes []string, all, install, dry bool) error {
	var targets []harness.JcodeInstance
	if all {
		if err := withApp(func(_ context.Context, a *app.App) error {
			targets = a.JcodeInstances()
			return nil
		}); err != nil {
			return err
		}
	}
	for _, h := range homes {
		targets = append(targets, harness.InstanceForHome(targets, h))
	}
	seen := map[string]bool{}
	for _, t := range targets {
		if seen[t.Home] {
			continue
		}
		seen[t.Home] = true
		r, err := harness.InstallJcodeHome(t.Home, "jcode["+t.Name+"]", bin, install, dry)
		if err != nil {
			return fmt.Errorf("jcode[%s]: %w", t.Name, err)
		}
		if dry {
			fmt.Print("[dry-run] ")
		}
		fmt.Println(r)
	}
	return nil
}

func jcodeCmd() *cobra.Command {
	c := &cobra.Command{Use: "jcode", Short: "jcode integration helpers"}
	c.AddCommand(&cobra.Command{Use: "instances", Short: "List jcode instances muninn knows about", RunE: func(*cobra.Command, []string) error {
		return withApp(func(ctx context.Context, a *app.App) error {
			counts := map[string]int{}
			rows, err := a.DB.QueryContext(ctx, `SELECT scope, COUNT(*) FROM units WHERE active=1 AND scope LIKE 'jcode@%' GROUP BY scope`)
			if err != nil {
				return err
			}
			for rows.Next() {
				var s string
				var n int
				rows.Scan(&s, &n)
				if i := strings.Index(s, ":"); i > 0 {
					s = s[:i]
				}
				counts[s] += n
			}
			rows.Close()
			fmt.Printf("%-26s %-8s %-6s %-9s %s\n", "INSTANCE", "MEMORIES", "BINARY", "HOOK", "JCODE_HOME")
			for _, i := range a.JcodeInstances() {
				hook := "no"
				if harness.JcodeHookInstalled(i.Home) {
					hook = "yes"
				}
				bin := "PATH"
				if i.Binary() != "" {
					bin = "own"
				}
				fmt.Printf("%-26s %-8d %-6s %-9s %s\n", i.Name, counts[i.Scope()], bin, hook, i.Home)
			}
			return nil
		})
	}})
	return c
}

func installCmd() *cobra.Command   { return installLike(true) }
func uninstallCmd() *cobra.Command { return installLike(false) }
