package standalone

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStandaloneServesOverStdioWithoutReadingOutside(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.py"), []byte("def canary_secret_fn():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.py"), []byte("def app_main():\n    helper()\n\ndef helper():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.py"), filepath.Join(root, "leak.py")); err != nil {
		t.Fatal(err)
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	cacheDir := filepath.Join(root, ".pi", "repomap-cache")
	go func() {
		done <- Run(context.Background(), root, Options{CacheDir: cacheDir}, inR, outW)
		outW.Close()
	}()
	out := bufio.NewScanner(outR)
	out.Buffer(make([]byte, 1<<20), 1<<20)
	call := func(id int, method string, params any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if _, err := inW.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
		if !out.Scan() {
			t.Fatalf("no response to %s: %v", method, out.Err())
		}
		var resp map[string]any
		if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
			t.Fatalf("bad response %q: %v", out.Text(), err)
		}
		if resp["error"] != nil {
			t.Fatalf("%s error: %v", method, resp["error"])
		}
		return resp
	}
	text := func(resp map[string]any) string {
		content := resp["result"].(map[string]any)["content"].([]any)
		return content[0].(map[string]any)["text"].(string)
	}

	call(1, "initialize", map[string]any{"protocolVersion": "2024-11-05"})
	tools := call(2, "tools/list", map[string]any{})
	if n := len(tools["result"].(map[string]any)["tools"].([]any)); n != 5 {
		t.Fatalf("want 5 tools, got %d", n)
	}
	got := text(call(3, "tools/call", map[string]any{"name": "search_identifiers", "arguments": map[string]any{"query": "helper"}}))
	if !strings.Contains(got, "app.py") {
		t.Fatalf("search_identifiers missed helper: %s", got)
	}
	if got := text(call(4, "tools/call", map[string]any{"name": "search_identifiers", "arguments": map[string]any{"query": "canary"}})); got != "null" && got != "[]" {
		t.Fatalf("identifier from outside the root served: %s", got)
	}
	if got := text(call(5, "tools/call", map[string]any{"name": "repo_map", "arguments": map[string]any{"map_tokens": 1024}})); !strings.Contains(got, "app_main") {
		t.Fatalf("repo_map missing app_main: %s", got)
	}

	inW.Close() // EOF on stdin ends the server
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit on stdin EOF")
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "tags.db")); err != nil {
		t.Fatalf("cache not written to CacheDir: %v", err)
	}
}

func TestStandaloneAgentMode(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.py"), []byte("def app_main():\n    helper()\n\ndef helper():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"callers","arguments":{"symbol":"helper"}}}`,
	}, "\n") + "\n"
	var out strings.Builder
	err := Run(context.Background(), root, Options{CacheDir: filepath.Join(root, ".cache"), Agent: true}, strings.NewReader(in), &out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 responses, got %d: %s", len(lines), out.String())
	}
	if !strings.Contains(lines[0], `"instructions"`) {
		t.Errorf("initialize lacks instructions: %s", lines[0])
	}
	for _, name := range []string{`"callers"`, `"outline"`, `"search"`} {
		if !strings.Contains(lines[1], name) {
			t.Errorf("tools/list lacks %s", name)
		}
	}
	if strings.Contains(lines[1], `"find_dead_code"`) {
		t.Error("agent default must not list the legacy tools")
	}
	if !strings.Contains(lines[2], "app.py:2 in app_main [1-2]") {
		t.Errorf("callers answer: %s", lines[2])
	}
	if err := Run(context.Background(), root, Options{Agent: true, Tools: []string{"bogus"}}, strings.NewReader(""), &out); err == nil {
		t.Error("an unknown --tools name must fail before serving")
	}
}
