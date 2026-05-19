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

	"github.com/yourusername/repomap-go/internal/parser"
)

// MCPVersion is the protocol version advertised in initialize responses.
const MCPVersion = "2024-11-05"

// ProjectAccessor is the narrow interface the MCP server needs from a project.
// Defined here to avoid importing internal/project (which would cycle).
type ProjectAccessor interface {
	RepoMap(tokenBudget int, chatFiles []string, forceRefresh bool) string
	SearchIdentifiers(query, filter string, limit int) []parser.Tag
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
		"capabilities":    map[string]any{"tools": map[string]any{}},
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
				},
				"required": []string{"project_root", "query"},
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
	}
	return nil, &RPCError{Code: codeMethodNotFound, Message: "unknown tool: " + call.Name}
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
	ProjectRoot string `json:"project_root"`
	Query       string `json:"query"`
	Filter      string `json:"filter"`
	Limit       int    `json:"limit"`
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
	tags := s.project.SearchIdentifiers(args.Query, args.Filter, args.Limit)
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
