package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/extract"
	"github.com/veridit/muninn/internal/scopetest"
)

func TestCaptureAuditCommands(t *testing.T) {
	f := scopetest.New(t)
	r := app.AuditRun{Time: time.Now().UTC(), Session: "synthetic-session", Harness: "stdin", Trigger: "manual", DryRun: true, Report: app.CaptureReport{Usage: extract.Usage{TotalTokens: 15}, Actions: []app.CaptureAction{{Candidate: extract.Candidate{Category: "decision", Trust: "high", Content: "Use quartz."}, Action: "add", Reason: "new"}}}}
	if err := f.App.SaveAudit(r); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{{[]string{"log", "--session", "synthetic-session", "--since", "24h"}, "synthetic-session"}, {[]string{"log", "1"}, `"dry_run": true`}, {[]string{"stats", "--since", "24h"}, `"decision/high/add": 1`}} {
		c := captureCmd()
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetArgs(tc.args)
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Fatalf("args=%v output=%s", tc.args, out.String())
		}
	}
	c := captureCmd()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"log", "--session", "absent", "--json"})
	if err := c.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	var runs []app.AuditRun
	if err := json.Unmarshal(out.Bytes(), &runs); err != nil || len(runs) != 0 {
		t.Fatalf("filter output=%s err=%v", out.String(), err)
	}
}
