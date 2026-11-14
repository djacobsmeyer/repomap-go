package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/djacobsmeyer/repomap-go/internal/agentq"
	"github.com/djacobsmeyer/repomap-go/internal/parser"
)

// Options select the tool surface a Server exposes.
type Options struct {
	// Agent switches to the surface for small-context agents: short
	// descriptions, no project_root parameter, plain-text answers within a
	// max_tokens budget, and the callers/outline/search tools. The default
	// (false) is the original surface, byte for byte.
	Agent bool
	// Tools, if non-empty, limits the tools listed and callable to these
	// names. Default: every legacy tool, or AgentDefaultTools in agent mode.
	Tools []string
}

// AgentDefaultTools is the agent-mode tool set when Options.Tools is empty.
var AgentDefaultTools = []string{"callers", "outline", "search"}

// legacyToolNames are the original five tools, in tools/list order.
var legacyToolNames = []string{"repo_map", "search_identifiers", "get_blast_radius", "find_dead_code", "get_changed_symbols"}

// agentOnlyToolNames exist only in agent mode.
var agentOnlyToolNames = []string{"callers", "outline", "search"}

// IndexAccessor is what the agent tools need from a project, beyond
// ProjectAccessor. *project.Project implements it.
type IndexAccessor interface {
	IndexSnapshot() (map[string][]parser.Tag, map[string]float32, map[string]int64)
	ReadSource(rel string) ([]byte, error)
}

// AgentHint is a short usage note for an agent's brief or system prompt,
// printed by `repomap hint`. With it in context, the tool descriptions can
// stay short. Keep it under ~150 tokens.
const AgentHint = `repomap indexes this worktree (.py .go .ts .js and python/node scripts; other text files are grepped).
- repomap_callers SYMBOL: every reference with file:line, enclosing function and its line range, and the line. Use it instead of grep + reading files. depth=2 adds who calls those functions: the whole two-hop impact, so stop there.
- repomap_outline PATH: definitions with line ranges, to pick a range before reading a big file.
- repomap_search QUERY: where a name is defined.
Answers are complete unless they end with [truncated ...].`

// NewWithOptions constructs a Server with a chosen tool surface. It fails on
// an unknown tool name, or an agent-only tool without Options.Agent.
func NewWithOptions(root string, p ProjectAccessor, opts Options) (*Server, error) {
	s := &Server{root: root, project: p, agent: opts.Agent}
	known := map[string]bool{}
	for _, n := range legacyToolNames {
		known[n] = true
	}
	if opts.Agent {
		for _, n := range agentOnlyToolNames {
			known[n] = true
		}
	}
	names := opts.Tools
	if len(names) == 0 && opts.Agent {
		names = AgentDefaultTools
	}
	if len(names) > 0 {
		s.enabled = map[string]bool{}
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if !known[n] {
				hint := ""
				for _, a := range agentOnlyToolNames {
					if a == n {
						hint = " (needs --agent)"
					}
				}
				return nil, fmt.Errorf("unknown tool %q%s; known: %s", n, hint, strings.Join(sortedNames(known), ", "))
			}
			s.enabled[n] = true
		}
	}
	if opts.Agent {
		if _, ok := p.(IndexAccessor); !ok {
			return nil, fmt.Errorf("agent mode needs a project that implements IndexAccessor")
		}
	}
	return s, nil
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *Server) toolEnabled(name string) bool {
	return s.enabled == nil || s.enabled[name]
}

// filterTools keeps the enabled tools of list, in order.
func (s *Server) filterTools(list []map[string]any) []map[string]any {
	if s.enabled == nil {
		return list
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		if s.enabled[t["name"].(string)] {
			out = append(out, t)
		}
	}
	return out
}

// --- flexible argument types -------------------------------------------------

// flexInt accepts 3, 3.0 and "3": agents (and some MCP clients) send numbers
// as strings.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if v, err := strconv.Atoi(s); err == nil {
		*f = flexInt(v)
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("not a number: %s", string(b))
	}
	*f = flexInt(int(v))
	return nil
}

// flexBool accepts true and "true" (and 1/"1"/"yes").
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	s := strings.ToLower(strings.Trim(strings.TrimSpace(string(b)), `"`))
	switch s {
	case "true", "1", "yes":
		*f = true
	case "false", "0", "no", "", "null":
		*f = false
	default:
		return fmt.Errorf("not a boolean: %s", string(b))
	}
	return nil
}

// --- agent tool list ---------------------------------------------------------

func prop(typ, desc string) map[string]any {
	m := map[string]any{"type": typ}
	if desc != "" {
		m["description"] = desc
	}
	return m
}

func obj(required []string, props map[string]any) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

// agentToolsList is the agent-mode surface. Descriptions are short on
// purpose: every tool schema is resent on every turn.
func (s *Server) agentToolsList() []map[string]any {
	budget := func(def int) map[string]any { return prop("integer", fmt.Sprintf("budget, default %d", def)) }
	all := []map[string]any{
		{
			"name":        "callers",
			"description": "All references to a symbol, each with file:line, enclosing function and its line range, and the line. Use instead of grep+read. depth=2: also callers of those functions.",
			"inputSchema": obj([]string{"symbol"}, map[string]any{
				"symbol":        prop("string", ""),
				"depth":         prop("integer", "1-3"),
				"context":       prop("integer", "extra lines, 0-3"),
				"include_tests": prop("boolean", ""),
				"path":          prop("string", "limit to dir/file"),
				"file":          prop("string", "defining file"),
				"max_tokens":    prop("integer", fmt.Sprintf("budget, default %d (%d if depth>1)", agentq.DefaultCallersTokens, agentq.DefaultCallersDeepTokens)),
			}),
		},
		{
			"name":        "outline",
			"description": "Definitions with line ranges in a file, or per-file sizes and definitions in a directory. Use before a ranged read.",
			"inputSchema": obj(nil, map[string]any{
				"path":          prop("string", "file or dir"),
				"all":           prop("boolean", "add variables"),
				"include_tests": prop("boolean", ""),
				"max_tokens":    budget(agentq.DefaultOutlineTokens),
			}),
		},
		{
			"name":        "search",
			"description": "Where names are defined (substring match); refs=true adds references.",
			// include_tests and path are accepted but not advertised: rarely
			// needed for a definition lookup, and each costs tokens per turn.
			"inputSchema": obj([]string{"query"}, map[string]any{
				"query":      prop("string", ""),
				"refs":       prop("boolean", ""),
				"max_tokens": budget(agentq.DefaultSearchTokens),
			}),
		},
		{
			"name":        "get_blast_radius",
			"description": "What depends on a symbol: references, then callers of their functions (by name), within max_tokens. full=true returns the file-level graph as JSON.",
			"inputSchema": obj([]string{"symbol"}, map[string]any{
				"symbol":     prop("string", ""),
				"file":       prop("string", ""),
				"depth":      prop("integer", "1-3, default 2"),
				"max_tokens": budget(agentq.DefaultCallersTokens),
				"full":       prop("boolean", ""),
			}),
		},
		{
			"name":        "repo_map",
			"description": "PageRank-ranked definitions across the repo.",
			"inputSchema": obj(nil, map[string]any{"map_tokens": prop("integer", "default 1024")}),
		},
		{
			"name":        "search_identifiers",
			"description": "Definitions/references by name substring, as JSON.",
			"inputSchema": obj([]string{"query"}, map[string]any{
				"query":  prop("string", ""),
				"filter": map[string]any{"type": "string", "enum": []string{"defs", "refs", "both"}},
				"limit":  prop("integer", "default 50"),
			}),
		},
		{
			"name":        "find_dead_code",
			"description": "Symbols never referenced and files nothing imports. Best: unexported_only=true, kinds=[\"function\",\"method\",\"class\"].",
			"inputSchema": obj(nil, map[string]any{
				"unexported_only": prop("boolean", ""),
				"kinds":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}),
		},
		{
			"name":        "get_changed_symbols",
			"description": "Definitions touched by a diff or by changes since git_ref.",
			"inputSchema": obj(nil, map[string]any{
				"git_ref": prop("string", ""),
				"diff":    prop("string", ""),
			}),
		},
	}
	return s.filterTools(all)
}

// --- agent tool handlers -----------------------------------------------------

func (s *Server) agentIndex() agentq.Index {
	ia := s.project.(IndexAccessor)
	tags, ranks, files := ia.IndexSnapshot()
	return agentq.Index{Tags: tags, Ranks: ranks, Files: files, Read: ia.ReadSource}
}

func textResult(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

func decode(raw json.RawMessage, v any) *RPCError {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	return nil
}

type agentCallersArgs struct {
	ProjectRoot  string   `json:"project_root"`
	Symbol       string   `json:"symbol"`
	File         string   `json:"file"`
	Depth        flexInt  `json:"depth"`
	Context      flexInt  `json:"context"`
	IncludeTests flexBool `json:"include_tests"`
	Path         string   `json:"path"`
	MaxTokens    flexInt  `json:"max_tokens"`
	Full         flexBool `json:"full"`
}

func (s *Server) agentCallers(raw json.RawMessage, defaultDepth int, allowFull bool) (any, *RPCError) {
	var a agentCallersArgs
	if e := decode(raw, &a); e != nil {
		return nil, e
	}
	if err := s.validateRoot(a.ProjectRoot); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(a.Symbol) == "" {
		return nil, &RPCError{Code: codeInvalidParams, Message: "symbol is required"}
	}
	if allowFull && bool(a.Full) {
		d := int(a.Depth)
		if d <= 0 {
			d = 3
		}
		if d > 10 {
			d = 10
		}
		res := s.project.BlastRadius(a.Symbol, a.File, d)
		body, err := json.Marshal(res)
		if err != nil {
			return nil, &RPCError{Code: codeInternalError, Message: err.Error()}
		}
		return textResult(string(body)), nil
	}
	depth := int(a.Depth)
	if depth <= 0 {
		depth = defaultDepth
	}
	var perr error
	if a.Path, perr = s.relPath(a.Path); perr == nil {
		a.File, perr = s.relPath(a.File)
	}
	if perr != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: perr.Error()}
	}
	out := agentq.Callers(s.agentIndex(), agentq.CallersOptions{
		Symbol: strings.TrimSpace(a.Symbol), File: a.File, Depth: depth, Context: int(a.Context),
		IncludeTests: bool(a.IncludeTests), Path: a.Path, MaxTokens: int(a.MaxTokens),
	})
	return textResult(out), nil
}

type agentOutlineArgs struct {
	ProjectRoot  string   `json:"project_root"`
	Path         string   `json:"path"`
	All          flexBool `json:"all"`
	IncludeTests flexBool `json:"include_tests"`
	MaxTokens    flexInt  `json:"max_tokens"`
}

func (s *Server) agentOutline(raw json.RawMessage) (any, *RPCError) {
	var a agentOutlineArgs
	if e := decode(raw, &a); e != nil {
		return nil, e
	}
	if err := s.validateRoot(a.ProjectRoot); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	p, err := s.relPath(a.Path)
	if err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	return textResult(agentq.Outline(s.agentIndex(), agentq.OutlineOptions{
		Path: p, All: bool(a.All), IncludeTests: bool(a.IncludeTests), MaxTokens: int(a.MaxTokens),
	})), nil
}

type agentSearchArgs struct {
	ProjectRoot  string   `json:"project_root"`
	Query        string   `json:"query"`
	Refs         flexBool `json:"refs"`
	IncludeTests flexBool `json:"include_tests"`
	Path         string   `json:"path"`
	MaxTokens    flexInt  `json:"max_tokens"`
}

func (s *Server) agentSearch(raw json.RawMessage) (any, *RPCError) {
	var a agentSearchArgs
	if e := decode(raw, &a); e != nil {
		return nil, e
	}
	if err := s.validateRoot(a.ProjectRoot); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
	}
	var perr error
	if a.Path, perr = s.relPath(a.Path); perr != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: perr.Error()}
	}
	return textResult(agentq.Search(s.agentIndex(), agentq.SearchOptions{
		Query: a.Query, Refs: bool(a.Refs), IncludeTests: bool(a.IncludeTests), Path: a.Path, MaxTokens: int(a.MaxTokens),
	})), nil
}

// agentLegacyArgs re-encodes agent-mode arguments of the legacy tools with
// flexible numbers/booleans and agent defaults, so the legacy handlers can
// take them.
func agentLegacyArgs(name string, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, &RPCError{Code: codeInvalidParams, Message: err.Error()}
		}
	}
	if m == nil {
		m = map[string]any{}
	}
	ints := map[string]bool{"map_tokens": true, "limit": true, "depth": true}
	bools := map[string]bool{"unexported_only": true, "exported_only": true, "force_refresh": true,
		"include_blast_radius": true, "include_test_orphans": true, "include_doc_orphans": true, "include_test_symbols": true}
	for k, v := range m {
		sv, isStr := v.(string)
		if !isStr {
			continue
		}
		if ints[k] {
			var f flexInt
			if err := f.UnmarshalJSON([]byte(strconv.Quote(sv))); err == nil {
				m[k] = int(f)
			}
		}
		if bools[k] {
			var f flexBool
			if err := f.UnmarshalJSON([]byte(strconv.Quote(sv))); err == nil {
				m[k] = bool(f)
			}
		}
	}
	if name == "repo_map" {
		if _, ok := m["map_tokens"]; !ok {
			m["map_tokens"] = 1024
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, &RPCError{Code: codeInternalError, Message: err.Error()}
	}
	return b, nil
}

// relPath turns an agent-supplied path (relative, or absolute inside the
// root) into a slash-separated path relative to the root.
func (s *Server) relPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if strings.HasPrefix(p, "/") {
		if p == s.root {
			return "", nil
		}
		if !strings.HasPrefix(p, s.root+"/") {
			return "", fmt.Errorf("path %s is outside the project %s", p, s.root)
		}
		p = strings.TrimPrefix(p, s.root+"/")
	}
	return p, nil
}
