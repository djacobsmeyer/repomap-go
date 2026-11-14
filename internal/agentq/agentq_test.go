package agentq

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/djacobsmeyer/repomap-go/internal/parser"
	"github.com/djacobsmeyer/repomap-go/internal/safefs"
)

// buildIndex writes files under a temp root and indexes them the way a
// project does: ParseFile for every file that may be source, Files for all.
func buildIndex(t *testing.T, files map[string]string) Index {
	t.Helper()
	root := t.TempDir()
	ix := Index{Tags: map[string][]parser.Tag{}, Files: map[string]int64{}, Ranks: map[string]float32{}}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		ix.Files[rel] = int64(len(body))
		if parser.MaybeSource(rel) {
			tags, _ := parser.ParseFile(root, rel)
			if len(tags) > 0 {
				ix.Tags[rel] = tags
			}
		}
	}
	ix.Read = func(rel string) ([]byte, error) { return safefs.ReadRegular(root, rel, 0) }
	return ix
}

var fixture = map[string]string{
	"lib/ledger.py": `"""ledger: declared_items is the entry point."""


def declared_items(profile, base_dir=""):
    return list(profile)


def summarize(profile):
    declared = declared_items(profile)
    return len(declared)
`,
	"app/run": `#!/usr/bin/env python3
import ledger


def main():
    items = ledger.declared_items({})
    return items


if __name__ == "__main__":
    main()
`,
	"app/svc.py": `from lib.ledger import declared_items


class Server:
    def __init__(self, profile):
        self.declared = declared_items(profile)

    def handle(self):
        return self.declared


def check_server(p):
    return Server(p)
`,
	"tests/test_ledger.py": `from lib.ledger import declared_items


def test_declared():
    assert declared_items([1]) == [1]
`,
	"scripts/run.sh": "#!/bin/sh\npython3 -c 'import ledger; ledger.declared_items({})'\n",
	"README.md":      "# Notes\n\nSee declared_items.\n",
}

func TestCallersListsSitesWithEnclosingFunctionAndLine(t *testing.T) {
	ix := buildIndex(t, fixture)
	out := Callers(ix, CallersOptions{Symbol: "declared_items"})
	for _, want := range []string{
		"declared_items: defined lib/ledger.py:4-5 function",
		`def declared_items(profile, base_dir=""):`,
		"app/run:6 in main [5-7]", // extensionless script, parsed via its shebang
		"items = ledger.declared_items({})",
		"app/svc.py:6 in Server.__init__ [5-6]",
		"self.declared = declared_items(profile)",
		"lib/ledger.py:9 in summarize [8-10]", // same-file caller (the old blast radius dropped it)
		"scripts/run.sh:2 (text)",             // unparsed file, grepped
		"2 more in tests",                     // the call and the import in tests/test_ledger.py
		"other mentions",
		"app/svc.py:1: from lib.ledger import declared_items", // the import line, with its text
		"[complete: every reference is listed above]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("callers output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tests/test_ledger.py") {
		t.Errorf("tests must be hidden by default:\n%s", out)
	}
	if strings.Contains(out, "README.md") {
		t.Errorf("markdown mentions are not code references:\n%s", out)
	}
	if strings.Contains(out, "[truncated") {
		t.Errorf("small answer must not say truncated:\n%s", out)
	}
}

func TestCallersIncludeTestsAndPath(t *testing.T) {
	ix := buildIndex(t, fixture)
	out := Callers(ix, CallersOptions{Symbol: "declared_items", IncludeTests: true})
	if !strings.Contains(out, "tests/test_ledger.py:5 in test_declared") {
		t.Errorf("include_tests: test call site missing:\n%s", out)
	}
	out = Callers(ix, CallersOptions{Symbol: "declared_items", Path: "app"})
	if strings.Contains(out, "lib/ledger.py:9") || !strings.Contains(out, "app/svc.py:6") {
		t.Errorf("path=app must keep only app/ sites:\n%s", out)
	}
}

func TestCallersContextLines(t *testing.T) {
	ix := buildIndex(t, fixture)
	out := Callers(ix, CallersOptions{Symbol: "declared_items", Context: 1, Path: "lib"})
	for _, want := range []string{" 8 def summarize(profile):", " >9 declared = declared_items(profile)", " 10 return len(declared)"} {
		if !strings.Contains(out, want) {
			t.Errorf("context=1 lacks %q:\n%s", want, out)
		}
	}
}

func TestCallersDepth2FollowsEnclosingFunctionsAndConstructors(t *testing.T) {
	ix := buildIndex(t, fixture)
	out := Callers(ix, CallersOptions{Symbol: "declared_items", Depth: 2})
	for _, want := range []string{
		"depth 2:",
		"Server (from app/svc.py:5)",            // __init__ -> its class
		"app/svc.py:13 in check_server [12-13]", // where Server() is built
		"main (from app/run:5)",
		"app/run:11 module level",
		"summarize (from lib/ledger.py:8): 0 references",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("depth 2 lacks %q:\n%s", want, out)
		}
	}
}

func TestCallersResolvesSameNameDefinitions(t *testing.T) {
	ix := buildIndex(t, map[string]string{
		"a.py": "def load_profile(p):\n    return p\n\n\ndef use_a():\n    return load_profile(1)\n",
		"b.py": "def load_profile(p):\n    return p\n\n\ndef use_b():\n    return load_profile(2)\n",
		"c.py": "import b\n\n\ndef via_b():\n    return b.load_profile(3)\n",
		"d.py": "import a\n\n\ndef via_a():\n    return a.load_profile(4)\n",
	})
	out := Callers(ix, CallersOptions{Symbol: "load_profile", File: "a.py"})
	if !strings.Contains(out, "a.py:6 in use_a") || !strings.Contains(out, "d.py:5 in via_a") {
		t.Errorf("a.py's own and a.-qualified references must stay:\n%s", out)
	}
	if strings.Contains(out, "b.py:6") || strings.Contains(out, "c.py:5") {
		t.Errorf("references resolving to b.py's load_profile must be dropped:\n%s", out)
	}
	if !strings.Contains(out, "2 resolved to the other definitions") {
		t.Errorf("summary must count the re-attributed references:\n%s", out)
	}
	// Without file=, both definitions are listed and every reference shown.
	out = Callers(ix, CallersOptions{Symbol: "load_profile"})
	if !strings.Contains(out, "2 definitions") || !strings.Contains(out, "c.py:5") {
		t.Errorf("unnarrowed query must list both definitions and all sites:\n%s", out)
	}
}

func TestCallersStaysWithinBudgetAndSaysTruncated(t *testing.T) {
	files := map[string]string{"lib.py": "def hot():\n    return 1\n"}
	for i := 0; i < 120; i++ {
		files[fmt.Sprintf("pkg/m%03d.py", i)] = fmt.Sprintf("from lib import hot\n\n\ndef user_%d():\n    return hot() + %d\n", i, i)
	}
	ix := buildIndex(t, files)
	out := Callers(ix, CallersOptions{Symbol: "hot", MaxTokens: 400})
	if got := EstimateTokens(out); got > 400 {
		t.Errorf("output is ~%d tokens, budget 400", got)
	}
	if strings.Contains(out, "[complete") {
		t.Errorf("truncated output must not say complete:\n%s", out)
	}
	if !strings.Contains(out, "120 references in 120 files") {
		t.Errorf("the totals must survive truncation:\n%s", out)
	}
	m := regexp.MustCompile(`\[truncated at max_tokens=400: (\d+) more references`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("truncation note missing:\n%s", out)
	}
	shown := strings.Count(out, " in user_")
	var more int
	fmt.Sscan(m[1], &more)
	if shown+more != 120+120 { // 120 call sites + 120 import mentions
		t.Errorf("shown %d + truncated %d should account for all 240 sites and mentions", shown, more)
	}
}

func TestCallersUnknownSymbol(t *testing.T) {
	ix := buildIndex(t, fixture)
	out := Callers(ix, CallersOptions{Symbol: "no_such_thing"})
	if !strings.Contains(out, "no definition found") || !strings.Contains(out, "0 references in 0 files") {
		t.Errorf("unexpected:\n%s", out)
	}
}

func TestOutlineFileIsNestedWithRanges(t *testing.T) {
	ix := buildIndex(t, fixture)
	out := Outline(ix, OutlineOptions{Path: "app/svc.py"})
	for _, want := range []string{
		"app/svc.py (13 lines, 4 definitions)",
		"\n  4-9 class Server\n",
		"\n    5-6 function __init__\n",
		"\n    8-9 function handle\n",
		"\n  12-13 function check_server\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("outline lacks %q:\n%s", want, out)
		}
	}
	// A path given as ./x or with a trailing slash resolves the same way.
	if Outline(ix, OutlineOptions{Path: "./app/svc.py"}) != out {
		t.Error("./ prefix should not change the result")
	}
}

func TestOutlineUnparsedFileAndDirectory(t *testing.T) {
	ix := buildIndex(t, fixture)
	if out := Outline(ix, OutlineOptions{Path: "scripts/run.sh"}); !strings.Contains(out, "2 lines, not parsed") {
		t.Errorf("unparsed file: %s", out)
	}
	out := Outline(ix, OutlineOptions{Path: "app/"})
	for _, want := range []string{"app/: 2 source files", "app/run (11 lines): main 5", "app/svc.py (13 lines): class Server 4, check_server 12"} {
		if !strings.Contains(out, want) {
			t.Errorf("dir outline lacks %q:\n%s", want, out)
		}
	}
	out = Outline(ix, OutlineOptions{})
	if !strings.Contains(out, "1 test files hidden") || strings.Contains(out, "test_ledger") {
		t.Errorf("root outline must hide tests:\n%s", out)
	}
}

func TestOutlineDirectoryBudget(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 80; i++ {
		var b strings.Builder
		for j := 0; j < 30; j++ {
			fmt.Fprintf(&b, "def function_number_%d_%d():\n    pass\n\n", i, j)
		}
		files[fmt.Sprintf("pkg/mod%02d.py", i)] = b.String()
	}
	ix := buildIndex(t, files)
	out := Outline(ix, OutlineOptions{Path: "pkg", MaxTokens: 500})
	if got := EstimateTokens(out); got > 500 {
		t.Errorf("~%d tokens, budget 500", got)
	}
	if !strings.Contains(out, "+18 more") {
		t.Errorf("per-file definitions must be capped at %d:\n%s", dirEntryMaxDefs, out)
	}
	if !strings.Contains(out, "lower-ranked files not shown") {
		t.Errorf("truncation note missing:\n%s", out)
	}
}

func TestSearchExactBeforeSubstringAndHidesTests(t *testing.T) {
	ix := buildIndex(t, map[string]string{
		"a.py":            "def load():\n    pass\n\n\ndef load_all():\n    pass\n\n\ndef preload():\n    pass\n",
		"tests/test_a.py": "def test_load():\n    pass\n",
	})
	out := Search(ix, SearchOptions{Query: "load"})
	i1, i2, i3 := strings.Index(out, "function load\n"), strings.Index(out, "load_all"), strings.Index(out, "preload")
	if i1 < 0 || i2 < i1 || i3 < i2 {
		t.Errorf("want exact, prefix, substring order:\n%s", out)
	}
	if !strings.Contains(out, "1 more in tests") || strings.Contains(out, "test_load") {
		t.Errorf("tests must be hidden and counted:\n%s", out)
	}
	if !strings.Contains(out, "a.py:1-2 function load") {
		t.Errorf("definitions carry their span:\n%s", out)
	}
}

func TestEstimateTokensCountsDigits(t *testing.T) {
	if EstimateTokens("1234") != 4 {
		t.Errorf("digits are one token each: %d", EstimateTokens("1234"))
	}
	if got := EstimateTokens(strings.Repeat("abcd", 80)); got < 95 || got > 105 {
		t.Errorf("320 letters should be ~100 tokens, got %d", got)
	}
}

// A/B t2: a depth-2 group for a method name other classes share used to say
// "N by-name matches not listed", and the agent went hunting for them.
func TestCallersDepth2ListsSharedMethodMatchesCompactly(t *testing.T) {
	ix := buildIndex(t, map[string]string{
		"core.py":  "def suspend():\n    pass\n",
		"life.py":  "from core import suspend\n\n\nclass Manager:\n    def shutdown(self):\n        suspend()\n\n    def close(self):\n        self.shutdown()\n",
		"other.py": "class Pool:\n    def shutdown(self):\n        pass\n",
		"use.py":   "def stop(pool):\n    pool.shutdown()\n", // which shutdown? unknowable by name
	})
	out := Callers(ix, CallersOptions{Symbol: "suspend", Depth: 2})
	for _, want := range []string{
		"life.py:9 in Manager.close [8-9]",
		"probably other classes' shutdown, listed last",
		"other shutdown (by name, not followed): use.py:2",
		"[complete: every reference to depth 2 is listed above]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
}

func TestCallersManyMentionsAreGroupedAndTruncatedIsNotComplete(t *testing.T) {
	var b strings.Builder
	b.WriteString("def hot():\n    pass\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "# hot is mentioned here %d\n", i)
	}
	ix := buildIndex(t, map[string]string{"lib.py": b.String()})
	out := Callers(ix, CallersOptions{Symbol: "hot"})
	if !strings.Contains(out, "  lib.py: 3, 4, 5") || strings.Contains(out, "lib.py:3: #") {
		t.Errorf("more than %d mentions should be grouped by file:\n%s", mentionsWithText, out)
	}
	out = Callers(ix, CallersOptions{Symbol: "hot", MaxTokens: 60})
	if strings.Contains(out, "[complete") || !strings.Contains(out, "[truncated") {
		t.Errorf("a truncated answer must not claim completeness:\n%s", out)
	}
}
