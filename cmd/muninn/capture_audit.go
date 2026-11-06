package main

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"github.com/veridit/muninn/internal/app"
	"strconv"
	"text/tabwriter"
	"time"
)

func captureAuditCmd(stats bool) *cobra.Command {
	var session, since string
	var asJSON bool
	use := "log [run-id]"
	short := "Browse local capture audit (contains private transcript text)"
	if stats {
		use = "stats"
		short = "Summarize local capture activity"
	}
	c := &cobra.Command{Use: use, Short: short, Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		duration, err := time.ParseDuration(since)
		if err != nil {
			return err
		}
		if duration < 0 {
			return fmt.Errorf("--since must be positive")
		}
		a, err := app.OpenAudit()
		if err != nil {
			return err
		}
		defer a.Close()
		runs, err := a.AuditRuns(session, time.Now().Add(-duration))
		if err != nil {
			return err
		}
		if len(args) > 0 {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return err
			}
			for _, r := range runs {
				if r.ID == id {
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetIndent("", "  ")
					return enc.Encode(r)
				}
			}
			return fmt.Errorf("capture run not found in selected interval")
		}
		if stats {
			result := struct {
				Runs      int            `json:"runs"`
				Tokens    int            `json:"tokens"`
				Errors    int            `json:"errors"`
				Proposals map[string]int `json:"proposals_category_trust_action"`
			}{Runs: len(runs), Proposals: map[string]int{}}
			for _, r := range runs {
				result.Tokens += r.Report.Usage.TotalTokens
				if r.Error != "" {
					result.Errors++
				}
				for _, v := range r.Report.Actions {
					result.Proposals[v.Category+"/"+v.Trust+"/"+v.Action]++
				}
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(runs)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tTIME\tHARNESS\tSESSION\tTRIGGER\tDRY\tPROPOSALS\tTOKENS\tERROR")
		for _, r := range runs {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%t\t%d\t%d\t%s\n", r.ID, r.Time.Format(time.RFC3339), r.Harness, r.Session, r.Trigger, r.DryRun, len(r.Report.Actions), r.Report.Usage.TotalTokens, r.Error)
		}
		return w.Flush()
	}}
	c.Flags().StringVar(&session, "session", "", "filter session identity")
	c.Flags().StringVar(&since, "since", "720h", "look back duration, e.g. 24h")
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return c
}
