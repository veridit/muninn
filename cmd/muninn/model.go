package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/embed"
)

func modelCmd() *cobra.Command {
	c := &cobra.Command{Use: "model", Short: "Manage the built-in embedding model (no Ollama needed)"}
	c.AddCommand(&cobra.Command{Use: "download", Short: "Download and verify the built-in model (~127 MB, once)",
		RunE: func(*cobra.Command, []string) error {
			b := embed.Builtin
			fmt.Fprintf(os.Stderr, "%s (%s@%s) -> %s\n", b.Name, b.Repo, b.Revision[:12], b.Dir(config.DataDir()))
			if err := b.Download(context.Background(), config.DataDir(), os.Stderr); err != nil {
				return err
			}
			fmt.Println("ready: every file matches its pinned sha256")
			return nil
		}})
	c.AddCommand(&cobra.Command{Use: "verify", Short: "Re-hash the built-in model files",
		RunE: func(*cobra.Command, []string) error {
			if err := embed.Builtin.Verify(config.DataDir()); err != nil {
				return err
			}
			fmt.Println("ok")
			return nil
		}})
	c.AddCommand(&cobra.Command{Use: "status", Short: "Show which embedder muninn uses now",
		RunE: func(*cobra.Command, []string) error {
			return withApp(func(_ context.Context, a *app.App) error {
				fmt.Println(a.EmbedNote)
				return nil
			})
		}})
	return c
}
