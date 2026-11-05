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
	"github.com/veridit/muninn/internal/harness"
)

func captureCmd() *cobra.Command {
	var session, home, harnessName string
	var dry, asJSON, hook bool
	c := &cobra.Command{Use: "capture", Short: "Extract durable memories from stdin or a jcode session (remote LLM by default)", RunE: func(cmd *cobra.Command, _ []string) error {
		if hook {
			session = os.Getenv("JCODE_HOOK_SESSION_ID")
			event := os.Getenv("JCODE_HOOK_EVENT")
			if event != "turn_end" && event != "session_end" {
				return errors.New("capture hook supports turn_end and session_end only")
			}
			harnessName = "jcode"
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
		seconds := cfg.Extract.TimeoutSeconds
		if seconds <= 0 {
			seconds = 45
		}
		if seconds > 120 {
			seconds = 120
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(seconds)*time.Second)
		defer cancel()
		run := func() error {
			if cfg.Extract.Provider == "off" {
				return nil
			}
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
			binding := a.Bound(home, cwd)
			if binding.Err != nil {
				return binding.Err
			}
			if _, err := a.Reindex(ctx); err != nil {
				return err
			}
			r, err := binding.Capture(ctx, a, app.CaptureOptions{Harness: harnessName, Session: session, Home: home, Messages: messages, DryRun: dry, Periodic: hook && os.Getenv("JCODE_HOOK_EVENT") == "turn_end"})
			if err != nil {
				return err
			}
			if hook {
				return captureLog(fmt.Sprintf("processed=%d proposals=%d rejected=%d tokens=%d latency_ms=%d skipped=%s", r.Processed, len(r.Actions), r.Rejected, r.Usage.TotalTokens, r.LatencyMS, r.Skipped))
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
		if hook && err != nil {
			// No transcript, key, filesystem path, response body, or provider error is
			// emitted from observers. Interactive runs retain safe diagnostic errors.
			_ = captureLog("capture failed (details withheld), pending messages will retry")
			return nil
		}
		return err
	}}
	c.Flags().StringVar(&session, "session", "", "session identity (required)")
	c.Flags().StringVar(&home, "jcode-home", "", "session's JCODE_HOME, defaults to environment/global")
	c.Flags().StringVar(&harnessName, "harness", "stdin", "transcript adapter: stdin or jcode")
	c.Flags().BoolVar(&dry, "dry-run", false, "propose actions without saving memories or advancing high-water marks")
	c.Flags().BoolVar(&asJSON, "json", false, "print proposals and usage as JSON")
	c.Flags().BoolVar(&hook, "hook", false, "jcode detached turn_end/session_end observer, log summaries only")
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
