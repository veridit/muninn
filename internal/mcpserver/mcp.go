// Package mcpserver exposes muninn over the Model Context Protocol.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/ingest"
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
	Content  string   `json:"content" jsonschema:"the fact, preference or correction to store"`
	Category string   `json:"category,omitempty" jsonschema:"fact | preference | correction | entity"`
	Scope    string   `json:"scope,omitempty" jsonschema:"global (default) or a project name"`
	Tags     []string `json:"tags,omitempty"`
}

type idOut struct {
	ID         string `json:"id"`
	Reinforced bool   `json:"reinforced,omitempty"`
	Suppressed bool   `json:"suppressed,omitempty"`
	Message    string `json:"message,omitempty"`
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

Recalled memories are dated notes, not documentation, tagged [date short-id] in [muninn recall] blocks. Prefer newer notes and verify before relying. Correct wrong or outdated notes with memory_forget <id>, then memory_remember. Ids accept a unique prefix of at least four characters or a legacy id. Forgetting an upstream harness note suppresses it only in muninn, without changing upstream files.

Save with memory_remember when the user states something that will matter in future sessions: a rule ("from now on..."), a preference, a decision, or a correction. One fact per memory, in the narrowest scope that fits. Never store secrets.`

// Options restrict what one MCP session may see and change. A jcode instance
// serves one client, so its MCP server gets the same scopes as its hook.
type Options struct {
	Scopes       []string // readable scopes; nil = unrestricted
	DefaultScope string   // scope for memory_remember without one; "" = global
}

func (o Options) allowed(scope string) bool {
	if len(o.Scopes) == 0 {
		return true
	}
	for _, s := range o.Scopes {
		if p, ok := strings.CutSuffix(s, "*"); ok && strings.HasPrefix(scope, p) || s == scope {
			return true
		}
	}
	return false
}

// New builds the MCP server over an opened app.
func New(a *app.App, version string, opt Options) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "muninn", Version: version}, &mcp.ServerOptions{Instructions: Instructions})

	mcp.AddTool(s, &mcp.Tool{Name: "memory_search", Description: "Search local memories (keyword + concept community + semantic). Use before answering questions about the user, their projects or past decisions."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, hitsOut, error) {
			o := a.RecallOptions()
			o.MinScore, o.Limit, o.Scopes = 0, in.Limit, opt.Scopes
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

	mcp.AddTool(s, &mcp.Tool{Name: "memory_remember", Description: "Store a durable memory: a rule, preference, decision or correction the user stated that will matter in future sessions. One fact per call. Never store secrets."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in rememberIn) (*mcp.CallToolResult, idOut, error) {
			if in.Category == "" {
				in.Category = "fact"
			}
			if in.Scope == "" {
				in.Scope = opt.DefaultScope
			}
			if in.Scope != "" && !opt.allowed(in.Scope) {
				return nil, idOut{}, fmt.Errorf("scope %q is not available in this session", in.Scope)
			}
			m, re, err := ingest.Remember(app.NativePath(), in.Content, in.Category, in.Scope, in.Tags)
			if err != nil {
				return nil, idOut{}, err
			}
			if _, err := a.Reindex(ctx); err != nil {
				return nil, idOut{}, err
			}
			u, err := a.DB.Resolve(m.ID)
			if err != nil {
				return nil, idOut{}, err
			}
			out := idOut{ID: u.SID, Reinforced: re}
			return text(out), out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "memory_forget", Description: "Retire a wrong or outdated memory by short id, unique prefix (4+ chars), or legacy id. Upstream harness notes are suppressed only in muninn, leaving upstream files unchanged. Follow with memory_remember for the correction."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in forgetIn) (*mcp.CallToolResult, idOut, error) {
			u, err := a.DB.Resolve(in.ID)
			if err != nil {
				return nil, idOut{}, err
			}
			if !opt.allowed(u.Scope) {
				return nil, idOut{}, fmt.Errorf("no visible memory %q", in.ID)
			}
			result, err := a.Forget(ctx, in.ID)
			if err != nil {
				return nil, idOut{}, err
			}
			out := idOut{ID: result.ID, Suppressed: result.Suppressed, Message: result.Message}
			return text(out), out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "memory_related", Description: "Nearest neighbours of a memory (precomputed, incremental kNN)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in relatedIn) (*mcp.CallToolResult, hitsOut, error) {
			if in.Limit <= 0 {
				in.Limit = 8
			}
			ref := in.ID
			if ref == "" {
				ref = fmt.Sprintf("#%d", in.UnitID)
			}
			u, err := a.DB.Resolve(ref)
			if err != nil {
				return nil, hitsOut{}, err
			}
			if !opt.allowed(u.Scope) {
				out := hitsOut{Results: []hit{}}
				return text(out), out, nil
			}
			res, err := a.Searcher.RelatedIn(ctx, u.ID, in.Limit, opt.Scopes)
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
			st, err := app.Stats(ctx, a)
			return text(st), st, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "memory_recall", Description: "The automatic-recall block muninn would inject for a message (thresholded, at most a few memories, may be empty)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Message string `json:"message"`
		}) (*mcp.CallToolResult, recall.Response, error) {
			resp, err := a.Recall.Recall(ctx, recall.Request{Message: in.Message, Harness: "mcp", Scopes: opt.Scopes})
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

// RunStdio serves MCP over stdin/stdout until the client disconnects.
func RunStdio(ctx context.Context, a *app.App, version string, opt Options) error {
	return New(a, version, opt).Run(ctx, &mcp.StdioTransport{})
}
