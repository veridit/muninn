package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/extract"
	"github.com/veridit/muninn/internal/harness"
)

func captureCmd() *cobra.Command {
	var session, home, harnessName string
	var dry, asJSON, hook, backfill bool
	c := &cobra.Command{Use: "capture", Short: "Extract durable memories from stdin or a jcode session (remote LLM by default)", RunE: func(cmd *cobra.Command, _ []string) (commandErr error) {
		audited := false
		defer func() {
			if !audited {
				if a, err := app.OpenAudit(); err == nil {
					r := app.AuditRun{Time: time.Now().UTC(), Harness: harnessName, Session: session, Home: home, Trigger: "manual", DryRun: dry, Model: a.Cfg.Extract.Model}
					if hook {
						r.Trigger = os.Getenv("JCODE_HOOK_EVENT")
					}
					if commandErr != nil {
						r.ErrorClass = extract.FailureClass(commandErr)
						r.Error = "capture failed before extraction (details withheld)"
					} else {
						r.Report.Skipped = "capture skipped before extraction"
					}
					if err := a.SaveAudit(r); err != nil && commandErr == nil {
						commandErr = err
					}
					a.Close()
				}
			}
			if hook && commandErr != nil {
				_ = captureLog("capture failed class=" + extract.FailureClass(commandErr) + ", pending messages will retry")
				commandErr = nil
			}
		}()
		if hook {
			session = os.Getenv("JCODE_HOOK_SESSION_ID")
			event := os.Getenv("JCODE_HOOK_EVENT")
			if event != "turn_end" && event != "session_end" {
				return errors.New("capture hook supports turn_end and session_end only")
			}
			harnessName = "jcode"
		}
		if hook && backfill {
			return errors.New("--backfill is manual only")
		}
		if home == "" {
			home = os.Getenv("JCODE_HOME")
		}
		if home == "" {
			u, _ := os.UserHomeDir()
			home = filepath.Join(u, ".jcode")
		}
		home, err := filepath.Abs(home)
		if err != nil {
			return err
		}
		// The complete command, including model probing/indexing, shares a hard
		// budget. Observer hooks are detached by jcode and never wait on this work.
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Extract.RunTimeout())
		defer cancel()
		run := func() error {
			cwd, _ := os.Getwd()
			var messages []harness.CaptureMessage
			if harnessName == "jcode" {
				if session == "" {
					return errors.New("--session is required for jcode capture")
				}
				t, err := harness.ReadCaptureSession(home, session)
				if err != nil {
					return err
				}
				messages = t.Messages
				cwd = t.Cwd
				if cwd == "" {
					return errors.New("session cwd missing")
				}
				if cfg.Extract.Skips(cwd) {
					if hook {
						return captureLog("skipped=skip_dirs")
					}
					fmt.Fprintln(cmd.OutOrStdout(), "skipped: session working directory matches extract.skip_dirs")
					return nil
				}
			} else {
				data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), (256<<10)+1))
				if err != nil {
					return err
				}
				if len(data) > 256<<10 {
					return errors.New("stdin transcript exceeds 256 KiB")
				}
				messages = harness.CaptureLines(string(data))
				if session == "" {
					return errors.New("--session is required for stdin high-water tracking")
				}
			}
			a, err := app.Open(ctx)
			if err != nil {
				return err
			}
			defer a.Close()
			if hook {
				a.Indexer.MaxEmbed = 1
			}
			binding := a.Bound(home, cwd)
			if binding.Err != nil {
				return binding.Err
			}
			// Capture reads the current index plus a fresh native snapshot. Full
			// graph/embedding work is deferred to recall/index, outside this hook.
			audited = true
			trigger := ""
			if hook {
				trigger = os.Getenv("JCODE_HOOK_EVENT")
			}
			r, err := binding.Capture(ctx, a, app.CaptureOptions{Harness: harnessName, Session: session, Home: home, Trigger: trigger, Messages: messages, DryRun: dry, Backfill: backfill, Periodic: hook && os.Getenv("JCODE_HOOK_EVENT") == "turn_end"})
			if err != nil {
				return err
			}
			if hook {
				return captureLog(fmt.Sprintf("processed=%d backlog_skipped=%d proposals=%d rejected=%d tokens=%d latency_ms=%d skipped=%s", r.Processed, r.BacklogSkipped, len(r.Actions), r.Rejected, r.Usage.TotalTokens, r.LatencyMS, r.Skipped))
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
			}
			if dry {
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "CATEGORY\tTRUST\tACTION\tCONTENT")
				for _, v := range r.Actions {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", v.Category, v.Trust, v.Action, strings.ReplaceAll(v.Content, "\t", " "))
				}
				w.Flush()
			}
			fmt.Fprintf(cmd.OutOrStdout(), "processed=%d proposals=%d rejected=%d tokens=%d latency_ms=%d", r.Processed, len(r.Actions), r.Rejected, r.Usage.TotalTokens, r.LatencyMS)
			if r.Skipped != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " skipped=%s", r.Skipped)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			return nil
		}
		err = run()
		return err
	}}
	c.Flags().StringVar(&session, "session", "", "session identity (required)")
	c.Flags().StringVar(&home, "jcode-home", "", "session's JCODE_HOME, defaults to environment/global")
	c.Flags().StringVar(&harnessName, "harness", "stdin", "transcript adapter: stdin or jcode")
	c.Flags().BoolVar(&backfill, "backfill", false, "manually process historical backlog skipped by initial hooks")
	c.Flags().BoolVar(&dry, "dry-run", false, "propose actions without saving memories or advancing high-water marks")
	c.Flags().BoolVar(&asJSON, "json", false, "print proposals and usage as JSON")
	c.Flags().BoolVar(&hook, "hook", false, "jcode detached turn_end/session_end observer, log summaries only")
	c.AddCommand(captureAuditCmd(false), captureAuditCmd(true))
	return c
}

func captureLog(message string) error {
	if err := os.MkdirAll(config.DataDir(), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(config.DataDir(), "hook.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s capture %s\n", time.Now().UTC().Format(time.RFC3339), message)
	return err
}
