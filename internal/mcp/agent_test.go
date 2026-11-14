package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djacobsmeyer/repomap-go/internal/agentq"
	"github.com/djacobsmeyer/repomap-go/internal/graph"
	"github.com/djacobsmeyer/repomap-go/internal/parser"
	"github.com/djacobsmeyer/repomap-go/internal/safefs"
)

// fakeProject indexes a temp dir with the real parser.
type fakeProject struct {
	root  string
	tags  map[string][]parser.Tag
	files map[string]int64
	blast int // BlastRadius calls
}

func newFake(t *testing.T, files map[string]string) *fakeProject {
	t.Helper()
	f := &fakeProject{root: t.TempDir(), tags: map[string][]parser.Tag{}, files: map[string]int64{}}
	for rel, body := range files {
		p := filepath.Join(f.root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		f.files[rel] = int64(len(body))
		if tags, _ := parser.ParseFile(f.root, rel); len(tags) > 0 {
			f.tags[rel] = tags
		}
	}
	return f
}

func (f *fakeProject) RepoMap(int, []string, bool) string { return "map" }
func (f *fakeProject) SearchIdentifiers(q, filter string, limit int, kinds []string) []parser.Tag {
	return nil
}
func (f *fakeProject) BlastRadius(symbol, file string, d int) graph.BlastRadiusResult {
	f.blast++
	return graph.BlastRadiusResult{Symbol: symbol, DefinedIn: "full-graph"}
}
func (f *fakeProject) FindDeadCode(graph.DeadCodeOptions) graph.DeadCodeResult {
	return graph.DeadCodeResult{}
}
func (f *fakeProject) ChangedSymbols(string, string, bool) graph.ChangedSymbolsResult {
	return graph.ChangedSymbolsResult{}
}
func (f *fakeProject) IndexSnapshot() (map[string][]parser.Tag, map[string]float32, map[string]int64) {
	return f.tags, nil, f.files
}
func (f *fakeProject) ReadSource(rel string) ([]byte, error) {
	return safefs.ReadRegular(f.root, rel, 0)
}

var demo = map[string]string{
	"lib.py": "def hot(x):\n    return x\n\n\ndef warm():\n    return hot(1)\n",
	"app.py": "import lib\n\n\ndef main():\n    return lib.hot(2)\n",
}

func call(t *testing.T, s *Server, name string, args any) (string, *RPCError) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	res, rpcErr := s.handleToolCall(raw)
	if rpcErr != nil {
		return "", rpcErr
	}
	return res.(map[string]any)["content"].([]map[string]any)[0]["text"].(string), nil
}

func toolNames(list []map[string]any) []string {
	var out []string
	for _, t := range list {
		out = append(out, t["name"].(string))
	}
	return out
}

// Existing Claude Code users of `repomap mcp` see exactly the old surface.
func TestLegacySurfaceUnchanged(t *testing.T) {
	golden, err := os.ReadFile("testdata/legacy_tools_list.json")
	if err != nil {
		t.Fatal(err)
	}
	var want any
	json.Unmarshal(golden, &want)
	for name, s := range map[string]*Server{
		"New":                       New("/r", nil),
		"NewWithOptions(Options{})": mustNew(t, Options{}),
	} {
		b, _ := json.Marshal(s.toolsList())
		var got any
		json.Unmarshal(b, &got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: tools/list differs from the pre-change golden", name)
		}
		if _, ok := s.initializeResult()["instructions"]; ok {
			t.Errorf("%s: legacy initialize must not grow an instructions field", name)
		}
	}
}

func mustNew(t *testing.T, o Options) *Server {
	t.Helper()
	s, err := NewWithOptions("/r", newFake(t, demo), o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAgentSurfaceIsSmall(t *testing.T) {
	s := mustNew(t, Options{Agent: true})
	if got := toolNames(s.toolsList()); !reflect.DeepEqual(got, AgentDefaultTools) {
		t.Fatalf("default agent tools = %v", got)
	}
	total := 0
	for _, tool := range s.toolsList() {
		if _, ok := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)["project_root"]; ok {
			t.Errorf("%s: agent schemas drop project_root (the server is bound to one root)", tool["name"])
		}
		b, _ := json.Marshal(map[string]any{"name": "repomap_" + tool["name"].(string), "description": tool["description"], "parameters": tool["inputSchema"]})
		total += agentq.EstimateTokens(string(b))
	}
	// 416 Qwen3.8 tokens measured; the legacy five are 1263.
	if total > 500 {
		t.Errorf("agent tool schemas ~%d tokens, want < 500", total)
	}
	if ins, _ := s.initializeResult()["instructions"].(string); ins != AgentHint {
		t.Error("agent initialize should carry the hint as MCP instructions")
	}
	if n := agentq.EstimateTokens(AgentHint); n > 200 {
		t.Errorf("AgentHint ~%d tokens, keep it under 200", n)
	}
}

func TestToolSelection(t *testing.T) {
	s := mustNew(t, Options{Agent: true, Tools: []string{"callers", "get_blast_radius"}})
	if got := toolNames(s.toolsList()); !reflect.DeepEqual(got, []string{"callers", "get_blast_radius"}) {
		t.Errorf("tools = %v", got)
	}
	if _, e := call(t, s, "outline", map[string]any{}); e == nil || e.Code != codeMethodNotFound {
		t.Errorf("a tool not selected must not be callable, got %v", e)
	}
	l := mustNew(t, Options{Tools: []string{"search_identifiers"}})
	if got := toolNames(l.toolsList()); !reflect.DeepEqual(got, []string{"search_identifiers"}) {
		t.Errorf("legacy --tools = %v", got)
	}
	if _, err := NewWithOptions("/r", newFake(t, demo), Options{Tools: []string{"callers"}}); err == nil || !strings.Contains(err.Error(), "needs --agent") {
		t.Errorf("callers without agent mode: %v", err)
	}
	if _, err := NewWithOptions("/r", newFake(t, demo), Options{Agent: true, Tools: []string{"nope"}}); err == nil {
		t.Error("unknown tool must fail")
	}
}

func TestAgentCallersAcceptsStringNumbersAndAbsolutePaths(t *testing.T) {
	f := newFake(t, demo)
	s, _ := NewWithOptions(f.root, f, Options{Agent: true})
	// The A/B agents sent {"depth": "10", "limit": "100"}: strings, not numbers.
	out, e := call(t, s, "callers", map[string]any{"symbol": "hot", "depth": "2", "max_tokens": "800", "include_tests": "true", "path": f.root})
	if e != nil {
		t.Fatal(e.Message)
	}
	for _, want := range []string{"app.py:5 in main [4-5]", "lib.py:6 in warm [5-6]", "depth 2:"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if _, e := call(t, s, "outline", map[string]any{"path": "/etc"}); e == nil {
		t.Error("a path outside the root must be refused")
	}
	if _, e := call(t, s, "callers", map[string]any{}); e == nil {
		t.Error("symbol is required")
	}
}

func TestAgentBlastRadiusCompactByDefaultFullOnRequest(t *testing.T) {
	f := newFake(t, demo)
	s, _ := NewWithOptions(f.root, f, Options{Agent: true, Tools: []string{"get_blast_radius"}})
	out, _ := call(t, s, "get_blast_radius", map[string]any{"symbol": "hot"})
	if !strings.Contains(out, "depth 2:") || f.blast != 0 {
		t.Errorf("default must be the compact depth-2 text, got:\n%s", out)
	}
	out, _ = call(t, s, "get_blast_radius", map[string]any{"symbol": "hot", "full": true})
	if !strings.Contains(out, "full-graph") || f.blast != 1 {
		t.Errorf("full=true must return the legacy JSON graph, got %s", out)
	}
}

func TestFlexArgs(t *testing.T) {
	var a struct {
		N flexInt  `json:"n"`
		B flexBool `json:"b"`
	}
	for in, want := range map[string][2]any{
		`{"n":3,"b":true}`:      {3, true},
		`{"n":"7","b":"false"}`: {7, false},
		`{"n":2.0,"b":"1"}`:     {2, true},
		`{"n":null,"b":null}`:   {0, false},
	} {
		a.N, a.B = 0, false
		if err := json.Unmarshal([]byte(in), &a); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if int(a.N) != want[0].(int) || bool(a.B) != want[1].(bool) {
			t.Errorf("%s -> %v %v", in, a.N, a.B)
		}
	}
	if err := json.Unmarshal([]byte(`{"n":"x"}`), &a); err == nil {
		t.Error("non-numeric string must fail")
	}
	raw, _ := agentLegacyArgs("repo_map", json.RawMessage(`{"force_refresh":"true"}`))
	if string(raw) != `{"force_refresh":true,"map_tokens":1024}` {
		t.Errorf("agent repo_map args = %s", raw)
	}
}
