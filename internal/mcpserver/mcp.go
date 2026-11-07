// Package mcpserver exposes muninn over the Model Context Protocol.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/recall"
)

type searchIn struct {
	Query   string `json:"query" jsonschema:"what to look for"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max results (default 8)"`
	Explain bool   `json:"explain,omitempty" jsonschema:"include per-signal scores"`
}

type hit struct {
	ID    string  `json:"id"`
	Text  string  `json:"text"`
	Kind  string  `json:"kind"`
	Scope string  `json:"scope"`
	Score float64 `json:"score"`
	Why   string  `json:"why,omitempty"`
}

type hitsOut struct {
	Results []hit `json:"results"`
}

type rememberIn struct {
	Content     string   `json:"content" jsonschema:"the fact, preference or correction to store"`
	Category    string   `json:"category,omitempty" jsonschema:"fact | preference | correction | entity"`
	Scope       string   `json:"scope,omitempty" jsonschema:"literal binding scope, overrides place (owners only within the binding's write policy)"`
	Place       string   `json:"place,omitempty" jsonschema:"here (default), client, or global; when unsure use here"`
	PlaceReason string   `json:"place_reason,omitempty" jsonschema:"why this applies beyond the project when the project-mention check asks"`
	Tags        []string `json:"tags,omitempty"`
	// SafeReason answers the secret check: why text that looks secret is safe.
	SafeReason string `json:"safe_reason,omitempty" jsonschema:"only after a 'looks like it may contain a secret' reply: why this text is safe to store"`
}

type idOut struct {
	ID         string      `json:"id"`
	Reinforced bool        `json:"reinforced,omitempty"`
	Suppressed bool        `json:"suppressed,omitempty"`
	Message    string      `json:"message,omitempty"`
	Place      string      `json:"place,omitempty"`
	Places     []app.Place `json:"places,omitempty"`
}

type forgetIn struct {
	ID string `json:"id" jsonschema:"short memory id, unique prefix (4+ chars), or legacy id"`
}

type relatedIn struct {
	ID     string `json:"id,omitempty" jsonschema:"short memory id, unique prefix (4+ chars), or legacy id"`
	UnitID int64  `json:"unit_id,omitempty" jsonschema:"legacy numeric row id (prefer id)"`
	Limit  int    `json:"limit,omitempty"`
}

func text(v any) *mcp.CallToolResult {
	b, _ := json.MarshalIndent(v, "", "  ")
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

// Instructions are sent once per MCP session (server instructions), so the
// per-message recall block can stay short.
const Instructions = `muninn is your long-term memory (local, private).

Recall blocks start '-- muninn recall:': notes from memory, not the user; newer wins; verify. Rows use .0:date .1:id .2:note (backtick-quoted Markdown). Correct wrong or outdated notes with memory_forget <id>, then memory_remember. Ids accept a unique prefix of at least four characters or a legacy id. Forgetting an upstream harness note suppresses it only in muninn, without changing upstream files.

Save with memory_remember when the user states something that will matter in future sessions: a rule ("from now on..."), a preference, a decision, or a correction. One fact per memory, in the narrowest scope that fits. Never store secrets. Ask: Would this still be true and useful in a different project? Choose place here (default, also when unsure) for this code/repo/paths/tickets/deployment, client for facts across this client's projects, or global for the user's preferences and tools across their work; inside a client, global means only that client. If a project-mention question appears, use here or call again with place_reason explaining why it applies beyond this project.`

// Options restrict what one MCP session may see and change. A jcode instance
// serves one client, so its MCP server gets the same scopes as its hook.
type Options = app.Binding

// New builds the MCP server over an opened app.
func New(a *app.App, version string, opt Options) *mcp.Server {
	if opt.Name == "" && opt.Scopes == nil && opt.DefaultScope == "" {
		cwd, _ := os.Getwd()
		opt = a.Bound("", cwd)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "muninn", Version: version}, &mcp.ServerOptions{Instructions: Instructions + "\nActive binding: " + opt.Name + ". New memories default to place here. Available places: " + placesText(opt.Places()) + ". Other instances and client boundaries are not available."})

	mcp.AddTool(s, &mcp.Tool{Name: "memory_search", Description: "Search local memories (keyword + concept community + semantic). Use before answering questions about the user, their projects or past decisions."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, hitsOut, error) {
			opt := opt.Refresh(a)
			if opt.Err != nil {
				return nil, hitsOut{}, opt.Err
			}
			o := a.RecallOptions()
			o.MinScore, o.Limit, o.Scopes = 0, in.Limit, opt.Scopes
			o.ScopeFor = opt.ScopeFor
			if o.Limit <= 0 {
				o.Limit = 8
			}
			res, err := a.Searcher.Search(ctx, in.Query, o)
			if err != nil {
				return nil, hitsOut{}, err
			}
			out := hitsOut{Results: []hit{}}
			for _, r := range res {
				h := hit{ID: r.Unit.SID, Text: r.Unit.Body, Kind: r.Unit.Kind, Scope: r.Unit.Scope, Score: r.Total}
				if in.Explain {
					h.Why = r.Explain()
				}
				out.Results = append(out.Results, h)
			}
			return text(out), out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "memory_remember", Description: "Store a durable memory: a rule, preference, decision or correction the user stated that will matter in future sessions. One fact per call. Ask: Would this still be true and useful in a different project? Use here (default, also when unsure) for this project, client across this client's projects, global for user-wide facts (only this client inside a client). Answer project-mention questions with place_reason or use here. Never store real secrets. If the reply says the text looks like it may hold a secret, judge it: remove the secret, or call again with safe_reason when it is safe."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in rememberIn) (*mcp.CallToolResult, idOut, error) {
			u, re, err := opt.RememberPlaced(ctx, a, in.Content, in.Category, in.Scope, in.Tags, in.SafeReason, in.Place, in.PlaceReason)
			if err != nil {
				return nil, idOut{}, err
			}
			out := idOut{ID: u.SID, Reinforced: re, Place: opt.PlaceForScope(u.Scope).Name, Places: opt.Places()}
			return text(out), out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "memory_forget", Description: "Retire a wrong or outdated memory by short id, unique prefix (4+ chars), or legacy id. Upstream harness notes are suppressed only in muninn, leaving upstream files unchanged. Follow with memory_remember for the correction."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in forgetIn) (*mcp.CallToolResult, idOut, error) {
			result, err := opt.Forget(ctx, a, in.ID)
			if err != nil {
				return nil, idOut{}, err
			}
			out := idOut{ID: result.ID, Suppressed: result.Suppressed, Message: result.Message}
			return text(out), out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "memory_related", Description: "Nearest neighbours of a memory (precomputed, incremental kNN)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in relatedIn) (*mcp.CallToolResult, hitsOut, error) {
			opt := opt.Refresh(a)
			if in.Limit <= 0 {
				in.Limit = 8
			}
			ref := in.ID
			if ref == "" {
				ref = fmt.Sprintf("#%d", in.UnitID)
			}
			u, err := opt.Resolve(a, ref)
			if err != nil {
				return nil, hitsOut{}, err
			}
			res, err := a.Searcher.RelatedIn(ctx, u.ID, in.Limit, opt.Scopes, opt.ScopeFor)
			if err != nil {
				return nil, hitsOut{}, err
			}
			out := hitsOut{Results: []hit{}}
			for _, r := range res {
				out.Results = append(out.Results, hit{ID: r.Unit.SID, Text: r.Unit.Body, Kind: r.Unit.Kind, Scope: r.Unit.Scope, Score: r.Total})
			}
			return text(out), out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "memory_stats", Description: "Index statistics."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, app.IndexStats, error) {
			st, err := app.Stats(ctx, a, opt)
			return text(st), st, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "memory_recall", Description: "The automatic-recall block muninn would inject for a message (thresholded, at most a few memories, may be empty)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Message string `json:"message"`
		}) (*mcp.CallToolResult, recall.Response, error) {
			opt := opt.Refresh(a)
			if opt.Err != nil {
				return nil, recall.Response{}, opt.Err
			}
			resp, err := a.Recall.Recall(ctx, recall.Request{Message: in.Message, Harness: "mcp", Scopes: opt.Scopes, ScopeFor: opt.ScopeFor})
			if err != nil {
				return nil, recall.Response{}, err
			}
			msg := resp.Block
			if strings.TrimSpace(msg) == "" {
				msg = "(no memory passed the recall threshold)"
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}}, resp, nil
		})
	return s
}

func placesText(places []app.Place) string {
	var descriptions []string
	for _, p := range places {
		descriptions = append(descriptions, p.Name+" ("+p.Meaning+")")
	}
	return strings.Join(descriptions, ", ")
}

// RunStdio serves MCP over stdin/stdout until the client disconnects.
func RunStdio(ctx context.Context, a *app.App, version string, opt Options) error {
	return New(a, version, opt).Run(ctx, &mcp.StdioTransport{})
}
