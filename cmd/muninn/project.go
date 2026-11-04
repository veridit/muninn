package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/project"
)

func projectCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{Use: "project", Short: "Show this directory's project identity (stable across renames, moves, clones)",
		RunE: func(*cobra.Command, []string) error {
			p, err := project.Find(dirOr(dir), false)
			if err != nil {
				return err
			}
			fmt.Printf("id:    %s\nlabel: %s\nroot:  %s\nscope: %s\nfile:  %s\n", p.ID, p.Label, p.Root, p.Scope(), p.File())
			return nil
		}}
	c.Flags().StringVar(&dir, "dir", "", "directory (default: current)")
	return c
}

func syncCmd() *cobra.Command {
	var dir string
	var dry bool
	c := &cobra.Command{Use: "sync", Short: "Sync this project's memories with .muninn/memories.jsonl (commit it to share with your team)",
		Long: `sync merges .muninn/memories.jsonl into your local store (idempotent: keyed by
the line id), then writes every memory in this project's scope back to the
file, sorted by id so git diffs and merges stay line-based.

Only memories in the project scope are written. Personal, global, client
and other projects' memories never leave your machine, and a memory that
looks like it contains a credential is withheld.`,
		RunE: func(*cobra.Command, []string) error {
			p, err := project.Find(dirOr(dir), !dry)
			if err != nil {
				return err
			}
			st, err := project.Sync(p, app.NativePath(), dry)
			if err != nil {
				return err
			}
			fmt.Printf("%s%s (%s): %s\n", dryTag(dry), p.Label, p.ID, st)
			return withApp(func(ctx context.Context, a *app.App) error { return reindexUnlessDry(ctx, a, dry) })
		}}
	c.Flags().StringVar(&dir, "dir", "", "directory (default: current)")
	c.Flags().BoolVar(&dry, "dry-run", false, "show counts without writing")
	return c
}

func dirOr(d string) string {
	if d != "" {
		return d
	}
	wd, _ := os.Getwd()
	return wd
}
