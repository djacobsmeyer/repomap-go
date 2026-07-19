// Package mcp implements a minimal JSON-RPC 2.0 server for the Model Context
// Protocol (MCP). One Server instance is bound to exactly one project; the
// daemon installs the Server's HandleConn as the per-project socket handler.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"

	"github.com/djacobsmeyer/repomap-go/internal/graph"
	"github.com/djacobsmeyer/repomap-go/internal/parser"
)

// MCPVersion is the protocol version advertised in initialize responses.
const MCPVersion = "2024-11-05"

// ProjectAccessor is the narrow interface the MCP server needs from a project.
// Defined here to avoid importing internal/project (which would cycle).
type ProjectAccessor interface {
	RepoMap(tokenBudget int, chatFiles []string, forceRefresh bool) string
	SearchIdentifiers(query, filter string, limit int, kinds []string) []parser.Tag
	BlastRadius(symbol, file string, maxDepth int) graph.BlastRadiusResult
	FindDeadCode(minRank float32, unexportedOnly, exportedOnly bool, kinds []string) graph.DeadCodeResult
	ChangedSymbols(diff, gitRef string, includeBlastRadius bool) graph.ChangedSymbolsResult
}

// Server handles MCP JSON-RPC 2.0 traffic for a single project.
type Server struct {
	root    string
	project ProjectAccessor
}

// New constructs a Server bound to a project at the given absolute root.
func New(root string, p ProjectAccessor) *Server {
	return &Server{root: root, project: p}
}

// --- JSON-RPC 2.0 wire types -------------------------------------------------

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Standard JSON-RPC error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// --- Connection handling -----------------------------------------------------

// HandleConn services one MCP client connection until EOF or error.
func (s *Server) HandleConn(conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			// Best-effort error reply, then bail.
			_ = enc.Encode(Response{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &RPCError{Code: codeParseError, Message: err.Error()},
			})
			return
		}

		// Notifications (no ID) get no response.
		isNotification := req.ID == nil
		resp := s.dispatch(req)
		if isNotification {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(req Request) Response {
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = s.initializeResult()
	case "initialized", "notifications/initialized":
		// Notification — caller ignores the response.
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": s.toolsList()}
	case "tools/call":
		result, rpcErr := s.handleToolCall(req.Params)
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
	case "prompts/list":
		resp.Result = map[string]any{"prompts": s.promptsList()}
	case "prompts/get":
		result, rpcErr := s.handlePromptGet(req.Params)
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
	case "ping":
		resp.Result = map[string]any{}
	default:
		resp.Error = &RPCError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
	}
	return resp
}

// --- initialize / tools list -------------------------------------------------

func (s *Server) initializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": MCPVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}, "prompts": map[string]any{}},
		"serverInfo":      map[string]any{"name": "repomap", "version": "0.1.0"},
		"tools":           s.toolsList(),
	}
}

func (s *Server) toolsList() []map[string]any {
	return []map[string]any{
		{
			"name":        "repo_map",
			"description": "Returns a ranked structural map of the codebase. Definitions only, sorted by PageRank.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_root":  map[string]any{"type": "string"},
					"map_tokens":    map[string]any{"type": "integer", "default": 8192},
					"chat_files":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"force_refresh": map[string]any{"type": "boolean", "default": false},
				},
				"required": []string{"project_root"},
			},
		},
		{
			"name":        "search_identifiers",
			"description": "Search for function/class/variable definitions or references by name.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_root": map[string]any{"type": "string"},
					"query":        map[string]any{"type": "string"},
					"filter":       map[string]any{"type": "string", "enum": []string{"defs", "refs", "both"}, "default": "both"},
					"limit":        map[string]any{"type": "integer", "default": 50},
					"kinds":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Filter by symbol kind: function, method, class, interface, type, variable, constant, def, ref"},
				},
				"required": []string{"project_root", "query"},
			},
		},
		{
			"name":        "get_blast_radius",
			"description": "Returns every file and symbol that transitively depends on the given symbol. Use before renaming or deleting anything.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_root": map[string]any{"type": "string"},
					"symbol":       map[string]any{"type": "string"},
					"file":         map[string]any{"type": "string", "description": "Optional: narrow to a specific definition file when symbol is overloaded"},
					"depth":        map[string]any{"type": "integer", "default": 3, "description": "Max traversal depth (1-10)"},
				},
				"required": []string{"project_root", "symbol"},
			},
		},
		{
			"name":        "find_dead_code",
			"description": "Returns symbols defined but never referenced in the project, and files with no importers. Note: exported symbols and reflection-called code may appear dead — filter to unexported names first for best results.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_root":    map[string]any{"type": "string"},
					"min_rank":        map[string]any{"type": "number", "default": 0.001, "description": "Exclude files below this PageRank (entry points and scripts legitimately have no callers)"},
					"unexported_only": map[string]any{"type": "boolean", "default": false, "description": "Only return unexported/private symbols. Best signal-to-noise for actionable dead code."},
					"exported_only":   map[string]any{"type": "boolean", "default": false, "description": "Only return exported/public symbols. High false-positive rate — external callers are invisible to static analysis."},
					"kinds":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Filter by symbol kind: function, method, class, interface, type, variable, constant, def, ref"},
				},
				"required": []string{"project_root"},
			},
		},
		{
			"name":        "get_changed_symbols",
			"description": "Given a git ref or unified diff, returns the symbols whose definitions fall within changed line ranges. Optionally includes blast radius for each changed symbol.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_root":         map[string]any{"type": "string"},
					"git_ref":              map[string]any{"type": "string", "description": "Git ref to diff against HEAD (e.g. 'HEAD~1', 'main'). Use this OR diff, not both."},
					"diff":                 map[string]any{"type": "string", "description": "Raw unified diff text. Use this OR git_ref, not both."},
					"include_blast_radius": map[string]any{"type": "boolean", "default": false, "description": "Also compute blast radius for each changed symbol (slower)"},
				},
				"required": []string{"project_root"},
			},
		},
	}
}

// --- tool call dispatch ------------------------------------------------------

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolCall(raw json.RawMessage) (any, *RPCError) {
	var call toolCallParams
	if err := json.Unmarshal(raw, &call); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	switch call.Name {
	case "repo_map":
		return s.toolRepoMap(call.Arguments)
	case "search_identifiers":
		return s.toolSearchIdentifiers(call.Arguments)
	case "get_blast_radius":
		return s.toolBlastRadius(call.Arguments)
	case "find_dead_code":
		return s.toolFindDeadCode(call.Arguments)
	case "get_changed_symbols":
		return s.toolChangedSymbols(call.Arguments)
	}
	return nil, &RPCError{Code: codeMethodNotFound, Message: "unknown tool: " + call.Name}
}

type blastRadiusArgs struct {
	ProjectRoot string `json:"project_root"`
	Symbol      string `json:"symbol"`
	File        string `json:"file"`
	Depth       int    `json:"depth"`
}

func (s *Server) toolBlastRadius(raw json.RawMessage) (any, *RPCError) {
	var args blastRadiusArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
		}
	}
	if err := s.validateRoot(args.ProjectRoot); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	if args.Symbol == "" {
		return nil, &RPCError{Code: codeInvalidParams, Message: "symbol is required"}
	}
	if args.Depth <= 0 {
		args.Depth = 3
	}
	if args.Depth > 10 {
		args.Depth = 10
	}
	res := s.project.BlastRadius(args.Symbol, args.File, args.Depth)
	body, err := json.Marshal(res)
	if err != nil {
		return nil, &RPCError{Code: codeInternalError, Message: err.Error()}
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(body)}}}, nil
}

type findDeadCodeArgs struct {
	ProjectRoot    string   `json:"project_root"`
	MinRank        float32  `json:"min_rank"`
	UnexportedOnly bool     `json:"unexported_only"`
	ExportedOnly   bool     `json:"exported_only"`
	Kinds          []string `json:"kinds"`
}

func (s *Server) toolFindDeadCode(raw json.RawMessage) (any, *RPCError) {
	args := findDeadCodeArgs{MinRank: 0.001}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
		}
	}
	if err := s.validateRoot(args.ProjectRoot); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	if args.UnexportedOnly && args.ExportedOnly {
		return nil, &RPCError{Code: codeInvalidParams, Message: "unexported_only and exported_only are mutually exclusive"}
	}
	if args.MinRank == 0 {
		args.MinRank = 0.001
	}
	res := s.project.FindDeadCode(args.MinRank, args.UnexportedOnly, args.ExportedOnly, args.Kinds)
	body, err := json.Marshal(res)
	if err != nil {
		return nil, &RPCError{Code: codeInternalError, Message: err.Error()}
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(body)}}}, nil
}

type changedSymbolsArgs struct {
	ProjectRoot        string `json:"project_root"`
	GitRef             string `json:"git_ref"`
	Diff               string `json:"diff"`
	IncludeBlastRadius bool   `json:"include_blast_radius"`
}

func (s *Server) toolChangedSymbols(raw json.RawMessage) (any, *RPCError) {
	var args changedSymbolsArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
		}
	}
	if err := s.validateRoot(args.ProjectRoot); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	if args.GitRef == "" && args.Diff == "" {
		return nil, &RPCError{Code: codeInvalidParams, Message: "either git_ref or diff must be provided"}
	}
	res := s.project.ChangedSymbols(args.Diff, args.GitRef, args.IncludeBlastRadius)
	body, err := json.Marshal(res)
	if err != nil {
		return nil, &RPCError{Code: codeInternalError, Message: err.Error()}
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(body)}}}, nil
}

type repoMapArgs struct {
	ProjectRoot  string   `json:"project_root"`
	MapTokens    int      `json:"map_tokens"`
	ChatFiles    []string `json:"chat_files"`
	ForceRefresh bool     `json:"force_refresh"`
}

func (s *Server) toolRepoMap(raw json.RawMessage) (any, *RPCError) {
	var args repoMapArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
		}
	}
	if err := s.validateRoot(args.ProjectRoot); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	if args.MapTokens <= 0 {
		args.MapTokens = 8192
	}
	out := s.project.RepoMap(args.MapTokens, args.ChatFiles, args.ForceRefresh)
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": out},
		},
	}, nil
}

type searchArgs struct {
	ProjectRoot string   `json:"project_root"`
	Query       string   `json:"query"`
	Filter      string   `json:"filter"`
	Limit       int      `json:"limit"`
	Kinds       []string `json:"kinds"`
}

func (s *Server) toolSearchIdentifiers(raw json.RawMessage) (any, *RPCError) {
	var args searchArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
		}
	}
	if err := s.validateRoot(args.ProjectRoot); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	if args.Filter == "" {
		args.Filter = "both"
	}
	if args.Limit <= 0 {
		args.Limit = 50
	}
	tags := s.project.SearchIdentifiers(args.Query, args.Filter, args.Limit, args.Kinds)
	body, err := json.Marshal(tags)
	if err != nil {
		return nil, &RPCError{Code: codeInternalError, Message: err.Error()}
	}
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(body)},
		},
	}, nil
}

// --- prompts ---------------------------------------------------------------

// promptGetParams is the arguments parsed from a prompts/get request.
type promptGetParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) promptsList() []map[string]any {
	return []map[string]any{
		{
			"name":        "explain",
			"description": "Teaches an agent how to use repomap effectively — daemon architecture, all 5 tools, SSE event stream, and worked examples.",
			"arguments": []map[string]any{
				{
					"name":        "project_root",
					"description": "Optional: restrict to a specific project root. Defaults to this server's bound project.",
					"required":    false,
				},
			},
		},
	}
}

func (s *Server) handlePromptGet(raw json.RawMessage) (any, *RPCError) {
	var params promptGetParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	if params.Name != "explain" {
		return nil, &RPCError{Code: codeInvalidParams, Message: "unknown prompt: " + params.Name}
	}

	var args struct {
		ProjectRoot string `json:"project_root"`
	}
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
		}
	}
	if err := s.validateRoot(args.ProjectRoot); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}

	return map[string]any{
		"description": "How to use repomap effectively",
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": map[string]any{"type": "text", "text": s.explainMessage()},
			},
		},
	}, nil
}

func (s *Server) explainMessage() string {
	var b strings.Builder
	bt := "`"
	in := func(s string) string { return bt + s + bt }

	b.WriteString("# repomap — How to Use This MCP Server\n\n")
	b.WriteString("## Architecture\n\n")
	b.WriteString("repomap runs as a **persistent background daemon** (one process). Each project gets its own goroutine, in-memory index, and fsnotify file watcher. The daemon exposes a Unix control socket and an SSE event stream on localhost:7374.\n\n")
	b.WriteString("Claude Code connects via a thin STDIO proxy (" + in("repomap mcp <path>") + ") that bridges to the daemon's per-project socket. The proxy is ephemeral; the daemon stays alive. If the daemon isn't already running, the proxy starts it automatically.\n\n")

	b.WriteString("## Auto-registration\n\n")
	b.WriteString("The **first MCP call to a new project automatically registers it** with the daemon. You do not need to run " + in("repomap add") + " manually — the proxy's " + in("mcp_connect") + " message triggers " + in("AddProject") + " on the daemon, which starts indexing and spawns the file watcher.\n\n")

	b.WriteString("## The 5 Tools\n\n")
	b.WriteString("| Tool | Required Params | Optional Params | When to Use |\n")
	b.WriteString("|------|----------------|-----------------|-------------|\n")
	b.WriteString("| " + in("repo_map") + " | " + in("project_root") + " | " + in("map_tokens") + " (default 8192), " + in("chat_files") + ", " + in("force_refresh") + " | Get a PageRank-sorted structural map of the project. Use for overview before any edit, or when you need the big picture. |\n")
	b.WriteString("| " + in("search_identifiers") + " | " + in("project_root") + ", " + in("query") + " | " + in("filter") + " (defs/refs/both), " + in("kinds") + " (function, method, class, etc.), " + in("limit") + " (default 50) | Find functions, classes, or variables by name. Use " + in("filter: \"defs\"") + " to see definitions only, " + in("\"refs\"") + " for callers. |\n")
	b.WriteString("| " + in("get_blast_radius") + " | " + in("project_root") + ", " + in("symbol") + " | " + in("file") + " (when symbol is overloaded), " + in("depth") + " (default 3, max 10) | Returns every file and symbol that transitively depends on the given symbol. **Call this BEFORE renaming or deleting** anything. |\n")
	b.WriteString("| " + in("find_dead_code") + " | " + in("project_root") + " | " + in("min_rank") + " (default 0.001), " + in("unexported_only") + " (default false), " + in("exported_only") + " (default false), " + in("kinds") + " | Returns symbols defined but never referenced. Use " + in("unexported_only: true") + " for best signal-to-noise (exported symbols and reflection-called code produce false positives). |\n")
	b.WriteString("| " + in("get_changed_symbols") + " | " + in("project_root") + " | " + in("git_ref") + " (e.g. \"HEAD~1\") OR " + in("diff") + " (raw unified diff), " + in("include_blast_radius") + " (default false) | Returns symbols whose definitions fall within changed line ranges. Use for PR reviews or before merging. |\n\n")

	b.WriteString("## SSE Event Stream\n\n")
	b.WriteString("For human monitoring, the daemon exposes a Server-Sent Events stream:\n\n")
	b.WriteString("- **CLI**: " + in("repomap events [--project <path>]") + " — pretty-printed, ANSI-colored\n")
	b.WriteString("- **curl**: " + in("curl -N http://localhost:7374/events") + " (raw SSE)\n\n")
	b.WriteString("Event types: " + in("project_added") + ", " + in("mcp_listener_error") + ", " + in("project_idle") + ", " + in("project_rehydrated") + ", " + in("reindex_started") + ", " + in("file_changed") + ", " + in("cache_hit") + ", " + in("reindex_complete") + ", " + in("mcp_call") + ".\n\n")
	b.WriteString("The daemon watches files with a 300ms debounce window — burst edits (save + lint + format) are collapsed into one reindex.\n\n")

	b.WriteString("## Worked Examples\n\n")
	b.WriteString("**Example 1: Before refactoring a function**\n\n")
	b.WriteString("You want to rename " + in("processPayment") + " to " + in("chargeOrder") + ". First, use " + in("get_blast_radius") + " to see all dependents:\n\n")
	b.WriteString("1. Call " + in("get_blast_radius") + " with " + in("symbol: \"processPayment\"") + " and " + in("depth: 5") + "\n")
	b.WriteString("2. Review the returned symbols and files\n")
	b.WriteString("3. For each affected file, call " + in("repo_map") + " with a smaller " + in("map_tokens") + " (e.g. 4096) to see the local context\n")
	b.WriteString("4. Make your changes\n\n")

	b.WriteString("**Example 2: Before merging a PR**\n\n")
	b.WriteString("You've committed changes on a feature branch and want to understand the impact:\n\n")
	b.WriteString("1. Call " + in("get_changed_symbols") + " with " + in("git_ref: \"main\"") + " and " + in("include_blast_radius: true") + "\n")
	b.WriteString("2. Review each changed symbol's blast radius\n")
	b.WriteString("3. For high-risk symbols, call " + in("repo_map") + " with " + in("force_refresh: true") + " to get the latest structural map\n\n")

	b.WriteString("**Example 3: Cleaning up old code**\n\n")
	b.WriteString("You suspect unused functions are accumulating:\n\n")
	b.WriteString("1. Call " + in("find_dead_code") + " with " + in("unexported_only: true") + "\n")
	b.WriteString("2. Review the returned list — these are symbols defined but never called\n")
	b.WriteString("3. For questionable cases, call " + in("search_identifiers") + " with " + in("filter: \"refs\"") + " to verify there are truly no callers\n\n")

	b.WriteString("## Quick Reference\n\n")
	b.WriteString("- " + in("repomap daemon start") + " — start the background daemon\n")
	b.WriteString("- " + in("repomap daemon status") + " — check uptime, active projects\n")
	b.WriteString("- " + in("repomap add <path>") + " — manually register a project (usually unnecessary)\n")
	b.WriteString("- " + in("repomap mcp <path>") + " — STDIO proxy (invoked by Claude Code)\n")
	b.WriteString("- " + in("repomap events") + " — tail SSE events to terminal\n")
	b.WriteString("- SSE: " + in("curl -N http://localhost:7374/events") + "\n")

	return b.String()
}

// validateRoot rejects requests whose project_root doesn't match this server's
// project. Empty is allowed (defaults to this server's project).
func (s *Server) validateRoot(want string) error {
	if want == "" {
		return nil
	}
	abs, err := filepath.Abs(want)
	if err != nil {
		return fmt.Errorf("invalid project_root: %w", err)
	}
	if abs != s.root {
		return fmt.Errorf("project_root mismatch: server bound to %s, request was %s", s.root, abs)
	}
	return nil
}