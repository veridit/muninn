package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/tidy"
)

// tidyCmd is registered only on the executable's owner CLI. It never uses a
// session binding for scanning and never reindexes or loads an embedder there.
func tidyCmd() *cobra.Command {
	root := &cobra.Command{Use: "tidy", Short: "Owner-only: review misplaced memories and deliberately apply moves"}
	o := tidy.Defaults()
	var out string
	var judge bool
	var maxJudge int
	scan := &cobra.Command{Use: "scan", Short: "Read the whole graph and write a review plan (no memory writes)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := o.Validate(); err != nil {
			return err
		}
		if maxJudge < 0 {
			return fmt.Errorf("--max-judge must be nonnegative")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		graph, err := tidy.ReadGraph(cmd.Context(), config.DBPath())
		if err != nil {
			return err
		}
		home, _ := os.UserHomeDir()
		cwd, _ := os.Getwd()
		bases := []string{cwd}
		if config.Home() == "" {
			bases = append(bases, home)
		}
		roots := tidy.DiscoverRoots(bases)
		plan, err := tidy.Scan(graph, o, roots)
		if err != nil {
			return err
		}
		plan.Database = config.DBPath()
		if judge {
			if err = tidy.Judge(cmd.Context(), &plan, cfg.Extract, maxJudge); err != nil {
				return err
			}
		}
		if out == "" {
			out = filepath.Join(config.DataDir(), "tidy", "plan-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".json")
		}
		if err = saveTidyPlan(out, plan); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%d active canonical units; %d suspects; %d maybe general\n", plan.Units, len(plan.Entries), len(plan.General))
		fmt.Fprintf(cmd.OutOrStdout(), "Classes: strong=%d group-only=%d vote-only=%d\n", plan.Classes["strong"], plan.Classes["group-only"], plan.Classes["vote-only"])
		for _, t := range plan.Transitions {
			fmt.Fprintf(cmd.OutOrStdout(), "%s -> %s [%s]: %d\n", t.Source, t.Target, t.Class, t.Count)
		}
		for _, g := range plan.Groups() {
			fmt.Fprintf(cmd.OutOrStdout(), "%s -> %s: %d (move=%d stay=%d unsure=%d)\n", g.Source, g.Target, g.Count, g.Decisions["move"], g.Decisions["stay"], g.Decisions["unsure"])
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Plan: %s\nSummary: %s.md\nReview before apply. No memory or index writes performed.\n", out, out)
		return nil
	}}
	scan.Flags().StringVar(&out, "out", "", "new JSON plan path (default data-dir/tidy/plan-<timestamp>.json)")
	scan.Flags().Float64Var(&o.MinSim, "min-sim", o.MinSim, "minimum neighbor similarity")
	scan.Flags().IntVar(&o.Majority, "majority", o.Majority, "minimum votes for another scope")
	scan.Flags().IntVar(&o.OwnMax, "own-max", o.OwnMax, "maximum votes for the current scope")
	scan.Flags().Float64Var(&o.GroupPurity, "group-purity", o.GroupPurity, "minimum share of a topic group's members in its owner scope")
	scan.Flags().IntVar(&o.GroupMin, "group-min", o.GroupMin, "minimum owner members in a client group (also at least 1% of the scope)")
	scan.Flags().IntVar(&o.GroupScore, "group-score", o.GroupScore, "minimum target client-group concept score (also at least 3x own score)")
	scan.Flags().BoolVar(&judge, "judge", false, "ask the configured capture model to judge proposals")
	scan.Flags().IntVar(&maxJudge, "max-judge", 100, "maximum suspects sent to the judge (strong, group-only, vote-only; 0 disables calls)")
	var dry bool
	var only string
	apply := &cobra.Command{Use: "apply PLAN", Short: "Apply reviewed move decisions, with backup and undo commands", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		p, err := tidy.Load(args[0])
		if err != nil {
			return err
		}
		if p.Database != "" && filepath.Clean(p.Database) != filepath.Clean(config.DBPath()) {
			return fmt.Errorf("plan belongs to a different database")
		}
		if dry {
			g, err := tidy.ReadGraph(cmd.Context(), config.DBPath())
			if err != nil {
				return err
			}
			entries, err := tidy.CheckSnapshot(p, g, only)
			if err != nil {
				return err
			}
			for _, e := range entries {
				fmt.Fprintf(cmd.OutOrStdout(), "would move %s: %s -> %s\n", e.SID, e.Source, e.Target)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Dry run: %d moves, no writes.\n", len(entries))
			return nil
		}
		return withApp(func(ctx context.Context, a *app.App) error {
			result, err := a.ApplyTidy(ctx, p, only, false)
			if err != nil {
				if result.Backup != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "Recovery backup: %s\n", result.Backup)
				}
				return err
			}
			for _, m := range result.Moves {
				fmt.Fprintf(cmd.OutOrStdout(), "moved %s: %s -> %s\nUndo placement: muninn rescope %s --to %s --cross-client\n", m.Ref, m.Source, m.Target, shellQuote(m.Ref), shellQuote(m.Source))
				if m.Upstream != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "Upstream %s remains unchanged and suppressed. Undo moves the native copy back, keeping upstream suppressed to avoid duplicates.\n", m.Upstream)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Moved %d memories. Backup and receipt: %s\n", len(result.Moves), result.Backup)
			return nil
		})
	}}
	apply.Flags().BoolVar(&dry, "dry-run", false, "validate and print moves without any writes")
	apply.Flags().StringVar(&only, "only", "", "apply only this exact SRC->DST pair (quote it in the shell)")
	root.AddCommand(scan, apply)
	return root
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func saveTidyPlan(path string, p tidy.Plan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	for _, item := range []struct {
		path string
		data []byte
	}{{path, append(raw, '\n')}, {path + ".md", []byte(p.Markdown())}} {
		f, err := os.OpenFile(item.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(item.data)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
