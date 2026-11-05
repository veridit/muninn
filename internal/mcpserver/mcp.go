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
	ID    int64   `json:"id"`
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
}

type forgetIn struct {
	ID string `json:"id" jsonschema:"muninn memory id (mem_...)"`
}

type relatedIn struct {
	UnitID int64 `json:"unit_id" jsonschema:"id from a search result"`
	Limit  int   `json:"limit,omitempty"`
}

func text(v any) *mcp.CallToolResult {
	b, _ := json.MarshalIndent(v, "", "  ")
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

// Instructions are sent once per MCP session (server instructions), so the
// per-message recall block can stay short.
const Instructions = `muninn is your long-term memory (local, private).

Recalled memories arrive in blocks marked [muninn recall], tagged [date id]. A memory is a dated note of what was believed when it was written, not documentation: prefer newer over older and check before relying on one. If one is wrong or outdated, fix it now: memory_forget its id, then memory_remember the corrected fact.

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
				h := hit{ID: r.Unit.ID, Text: r.Unit.Body, Kind: r.Unit.Kind, Scope: r.Unit.Scope, Score: r.Total}
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
			out := idOut{ID: m.ID, Reinforced: re}
			return text(out), out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "memory_forget", Description: "Retire a wrong or outdated muninn memory by its id (mem_... or imp_..., as shown in [muninn recall]). Follow with memory_remember for the corrected fact."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in forgetIn) (*mcp.CallToolResult, idOut, error) {
			if len(opt.Scopes) > 0 {
				mems, err := ingest.LoadNative(app.NativePath())
				if err != nil {
					return nil, idOut{}, err
				}
				visible := false
				for _, m := range mems {
					if m.ID == in.ID && opt.allowed(m.Scope) {
						visible = true
					}
				}
				if !visible {
					return nil, idOut{}, fmt.Errorf("no active muninn memory %q", in.ID)
				}
			}
			ok, err := ingest.Forget(app.NativePath(), in.ID)
			if err != nil {
				return nil, idOut{}, err
			}
			if !ok {
				return nil, idOut{}, fmt.Errorf("no active muninn memory %q", in.ID)
			}
			if _, err := a.Reindex(ctx); err != nil {
				return nil, idOut{}, err
			}
			return text(idOut{ID: in.ID}), idOut{ID: in.ID}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "memory_related", Description: "Nearest neighbours of a memory (precomputed, incremental kNN)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in relatedIn) (*mcp.CallToolResult, hitsOut, error) {
			if in.Limit <= 0 {
				in.Limit = 8
			}
			res, err := a.Searcher.RelatedIn(ctx, in.UnitID, in.Limit, opt.Scopes)
			if err != nil {
				return nil, hitsOut{}, err
			}
			out := hitsOut{Results: []hit{}}
			for _, r := range res {
				out.Results = append(out.Results, hit{ID: r.Unit.ID, Text: r.Unit.Body, Kind: r.Unit.Kind, Scope: r.Unit.Scope, Score: r.Total})
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
