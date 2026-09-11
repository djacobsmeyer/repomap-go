package mcp

import (
	"strings"
	"testing"
)

// TestFindDeadCodeSchemaGH1 verifies the MCP tool schema for find_dead_code
// after the GH-1 fix: the new orphan-visibility flags must be advertised,
// and the description must steer callers to the kinds recipe.
func TestFindDeadCodeSchemaGH1(t *testing.T) {
	s := New("/nonexistent-root", nil)

	var tool map[string]any
	for _, t := range s.toolsList() {
		if t["name"] == "find_dead_code" {
			tool = t
			break
		}
	}
	if tool == nil {
		t.Fatal("tools list does not contain find_dead_code")
	}

	schema, ok := tool["inputSchema"].(map[string]any)
	if !ok {
		t.Fatal("find_dead_code has no inputSchema object")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("find_dead_code inputSchema has no properties object")
	}
	for _, p := range []string{"include_test_orphans", "include_doc_orphans", "include_test_symbols"} {
		if _, ok := props[p]; !ok {
			t.Errorf("find_dead_code schema missing property %q", p)
		}
	}

	minRank, ok := props["min_rank"].(map[string]any)
	if !ok {
		t.Fatal("find_dead_code schema has no min_rank object")
	}
	mrDesc, _ := minRank["description"].(string)
	if !strings.Contains(mrDesc, "at or below") {
		t.Errorf("min_rank description must say PageRank 'at or below' the value; got: %s", mrDesc)
	}

	desc, _ := tool["description"].(string)
	if !strings.Contains(desc, "kinds") {
		t.Errorf("find_dead_code description does not mention %q; got: %s", "kinds", desc)
	}
}

// TestGetChangedSymbolsSchemaGH4 verifies the MCP tool schema for
// get_changed_symbols after the GH-4 fix: the description must advertise
// span-based (enclosing-definition) attribution and the reason field.
func TestGetChangedSymbolsSchemaGH4(t *testing.T) {
	s := New("/nonexistent-root", nil)

	var tool map[string]any
	for _, t := range s.toolsList() {
		if t["name"] == "get_changed_symbols" {
			tool = t
			break
		}
	}
	if tool == nil {
		t.Fatal("tools list does not contain get_changed_symbols")
	}

	desc, _ := tool["description"].(string)
	if !strings.Contains(desc, "enclosing") {
		t.Errorf("get_changed_symbols description must mention %q; got: %s", "enclosing", desc)
	}
	if !strings.Contains(desc, "reason") {
		t.Errorf("get_changed_symbols description must mention the reason field; got: %s", desc)
	}
}
