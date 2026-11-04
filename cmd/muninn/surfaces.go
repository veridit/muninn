package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/daemon"
	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/mcpserver"
	"github.com/veridit/muninn/internal/tui"
	"github.com/veridit/muninn/internal/web"
)

func ioReadAll(r io.Reader) ([]byte, error) { return io.ReadAll(io.LimitReader(r, 4<<20)) }

func serveCmd() *cobra.Command {
	var addr string
	var noWatch bool
	c := &cobra.Command{Use: "serve", Short: "Run the watcher (incremental reindex on change) and the local web UI",
		RunE: func(*cobra.Command, []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return withApp(func(_ context.Context, a *app.App) error {
				if addr == "" {
					addr = a.Cfg.ListenAddr
				}
				mu := &sync.Mutex{}
				if !noWatch {
					go func() {
						if err := daemon.Watch(ctx, a, mu, daemon.StdLog); err != nil {
							daemon.StdLog("watcher stopped: %v", err)
						}
					}()
				}
				fmt.Printf("muninn web UI on http://%s\n", addr)
				return web.ListenAndServe(ctx, addr, (&web.Server{App: a, Mu: mu}).Handler())
			})
		}}
	c.Flags().StringVar(&addr, "addr", "", "loopback listen address (default from config, 127.0.0.1:7431)")
	c.Flags().BoolVar(&noWatch, "no-watch", false, "do not watch sources")
	return c
}

func mcpCmd() *cobra.Command {
	return &cobra.Command{Use: "mcp", Short: "Serve MCP over stdio (memory_search, memory_remember, ...)",
		RunE: func(*cobra.Command, []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return withApp(func(_ context.Context, a *app.App) error {
				if _, err := a.Reindex(ctx); err != nil {
					fmt.Fprintln(os.Stderr, "muninn: reindex:", err)
				}
				return mcpserver.RunStdio(ctx, a, version)
			})
		}}
}

func tuiCmd() *cobra.Command {
	return &cobra.Command{Use: "tui", Short: "Interactive terminal browser", RunE: func(*cobra.Command, []string) error {
		return withApp(func(ctx context.Context, a *app.App) error {
			if _, err := a.Reindex(ctx); err != nil {
				return err
			}
			return tui.Run(a)
		})
	}}
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
	var dry bool
	var bin string
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
		return nil
	}}
	c.Flags().BoolVar(&dry, "dry-run", false, "show what would change")
	c.Flags().StringVar(&bin, "bin", "", "muninn binary path to put in the hook (default: this binary)")
	return c
}

func installCmd() *cobra.Command   { return installLike(true) }
func uninstallCmd() *cobra.Command { return installLike(false) }
