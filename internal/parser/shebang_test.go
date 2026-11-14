package parser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShebangLang(t *testing.T) {
	cases := map[string]string{
		"#!/usr/bin/env python3\nimport os\n":       "python",
		"#!/usr/bin/python3.12\n":                   "python",
		"#!/usr/bin/env -S python3 -u\n":            "python",
		"#!/usr/bin/env PYTHONSAFEPATH=1 python3\n": "python",
		"#!/usr/local/bin/pypy3\n":                  "python",
		"#!/usr/bin/env node\n":                     "javascript",
		"#!/usr/bin/env -S deno run\n":              "typescript",
		"#!/bin/bash\n":                             "",
		"#!/usr/bin/env bash\n":                     "",
		"# not a shebang\n":                         "",
		"":                                          "",
		"#!":                                        "",
	}
	for head, want := range cases {
		if got := ShebangLang([]byte(head)); got != want {
			t.Errorf("ShebangLang(%q) = %q, want %q", head, got, want)
		}
	}
}

func TestMaybeSource(t *testing.T) {
	for rel, want := range map[string]bool{
		"a.py": true, "sandbox/agent-queue": true, "Makefile": true, // extensionless: sniffed
		"x.json": false, "notes.txt": false, ".bashrc": false, "dir/.env": false,
	} {
		if got := MaybeSource(rel); got != want {
			t.Errorf("MaybeSource(%q) = %v, want %v", rel, got, want)
		}
	}
}

// Extensionless scripts (agent-queue, verify-run, ...) were invisible to
// repomap: in local-llm-stack, 3 of the 5 callers of the A/B symbol.
func TestParseFileExtensionlessPythonScript(t *testing.T) {
	root := t.TempDir()
	body := "#!/usr/bin/env python3\nimport work_ledger\n\n\ndef main():\n    return work_ledger.declared_items({})\n"
	if err := os.WriteFile(filepath.Join(root, "verify-run"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "build"), []byte("#!/bin/sh\ndeclared_items\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tags, err := ParseFile(root, "verify-run")
	if err != nil {
		t.Fatal(err)
	}
	var def, ref bool
	for _, tg := range tags {
		if tg.Name == "main" && tg.Kind == "function" && tg.Lang == "python" && tg.EndLine == 6 {
			def = true
		}
		if tg.Name == "declared_items" && tg.Kind == "ref" && tg.Line == 6 {
			ref = true
		}
	}
	if !def || !ref {
		t.Errorf("want def main 5-6 and ref declared_items:6, got %+v", tags)
	}
	if tags, _ := ParseFile(root, "build"); len(tags) != 0 {
		t.Errorf("a shell script has no parser, got %+v", tags)
	}
}

func TestParseFileShebangNeverReadsThroughSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("#!/usr/bin/env python3\ndef canary_fn():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "leak")); err != nil {
		t.Fatal(err)
	}
	if tags, _ := ParseFile(root, "leak"); len(tags) != 0 {
		t.Errorf("symlinked extensionless file must not be parsed: %+v", tags)
	}
}
