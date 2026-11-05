package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/ingest"
)

func importCmd() *cobra.Command {
	c := &cobra.Command{Use: "import", Short: "Copy memories from another memory system into muninn (idempotent)"}
	var dry bool
	c.PersistentFlags().BoolVar(&dry, "dry-run", false, "show counts without writing")

	var homes []string
	var all bool
	jc := &cobra.Command{Use: "jcode", Short: "Import jcode memories (default ~/.jcode; --all-instances or --jcode-home)",
		RunE: func(*cobra.Command, []string) error {
			return withApp(func(ctx context.Context, a *app.App) error {
				cwd, _ := os.Getwd()
				binding := a.Bound("", cwd)
				var targets []harness.JcodeInstance
				known := a.JcodeInstances()
				if all {
					targets = known
				}
				for _, h := range homes {
					targets = append(targets, harness.InstanceForHome(known, h))
				}
				if len(targets) == 0 {
					home := os.Getenv("JCODE_HOME")
					if home == "" {
						user, _ := os.UserHomeDir()
						home = filepath.Join(user, ".jcode")
					}
					targets = append(targets, harness.InstanceForHome(known, home))
				}
				seen := map[string]bool{}
				for _, t := range targets {
					if seen[t.Home] {
						continue
					}
					seen[t.Home] = true
					items, err := ingest.JcodeImportItems(filepath.Join(t.Home, "memory"), t.Scope())
					if err != nil {
						return err
					}
					st, err := binding.MergeImport(app.NativePath(), items, dry, filepath.Join(t.Home, "memory"))
					if err != nil {
						return err
					}
					fmt.Printf("%sjcode[%s]: %d read, %s\n", dryTag(dry), t.Name, len(items), st)
				}
				return reindexUnlessDry(ctx, a, dry)
			})
		}}
	jc.Flags().BoolVar(&all, "all-instances", false, "every known jcode instance")
	jc.Flags().StringSliceVar(&homes, "jcode-home", nil, "a specific JCODE_HOME (repeatable)")

	var file, dbPath string
	var maps []string
	en := &cobra.Command{Use: "engram", Short: "Import engram observations (from `engram export` JSON or engram.db, read-only)",
		RunE: func(*cobra.Command, []string) error {
			pm := map[string]string{}
			for _, m := range maps {
				k, v, ok := strings.Cut(m, "=")
				if !ok || k == "" || v == "" {
					return fmt.Errorf("--map wants project=scope, got %q", m)
				}
				pm[k] = v
			}
			var obs []ingest.EngramObservation
			var err error
			src := file
			if file != "" {
				obs, err = ingest.ReadEngramExport(file)
			} else {
				if dbPath == "" {
					home, _ := os.UserHomeDir()
					dbPath = filepath.Join(home, ".engram", "engram.db")
				}
				src = dbPath
				obs, err = ingest.ReadEngramDB(dbPath)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", src, err)
			}
			items := ingest.EngramItems(obs, ingest.EngramOptions{ProjectScopes: pm})
			return withApp(func(ctx context.Context, a *app.App) error {
				cwd, _ := os.Getwd()
				st, err := a.Bound("", cwd).MergeImport(app.NativePath(), items, dry)
				if err != nil {
					return err
				}
				fmt.Printf("%sengram (%s): %d read, %s\n", dryTag(dry), src, len(items), st)
				return reindexUnlessDry(ctx, a, dry)
			})
		}}
	en.Flags().StringVar(&file, "file", "", "an `engram export` JSON file")
	en.Flags().StringVar(&dbPath, "db", "", "engram database (default ~/.engram/engram.db)")
	en.Flags().StringSliceVar(&maps, "map", nil, "project=scope, e.g. statbus=jcode@statbus (repeatable; default engram:<project>)")

	c.AddCommand(jc, en)
	return c
}

func dryTag(dry bool) string {
	if dry {
		return "[dry-run] "
	}
	return ""
}

func reindexUnlessDry(ctx context.Context, a *app.App, dry bool) error {
	if dry {
		return nil
	}
	st, err := a.Reindex(ctx)
	if err == nil {
		fmt.Println("index:", st)
	}
	return err
}
