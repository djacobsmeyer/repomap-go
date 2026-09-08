package graph

import (
	"strings"
	"testing"

	"github.com/djacobsmeyer/repomap-go/internal/parser"
)

func TestClassifyFile(t *testing.T) {
	cases := []struct {
		relpath      string
		wantLang     string
		wantCategory string
	}{
		{"docs/README.md", "markdown", "docs"},
		{"tests/test_foo.py", "python", "test"},
		{"foo_test.go", "go", "test"},
		{"src/a.spec.ts", "typescript", "test"},
		{"conftest.py", "python", "test"},
		{"pkg/testdata/x.py", "python", "test"},
		{"src/engine.py", "python", "source"},
		{"internal/x.go", "go", "source"},
	}
	for _, tc := range cases {
		lang, category := ClassifyFile(tc.relpath)
		if lang != tc.wantLang {
			t.Errorf("ClassifyFile(%q) lang = %q, want %q", tc.relpath, lang, tc.wantLang)
		}
		if category != tc.wantCategory {
			t.Errorf("ClassifyFile(%q) category = %q, want %q", tc.relpath, category, tc.wantCategory)
		}
	}
}

// orphanFixtures builds a graph of three unreferenced files: one source
// orphan, one test orphan, and one docs orphan. No file references another,
// so all three have zero inbound edges (orphans).
func orphanFixtures() (map[string][]parser.Tag, map[string]float32, *FileGraph) {
	tags := map[string][]parser.Tag{
		"src/engine.py": {
			{RelFile: "src/engine.py", Line: 1, Name: "Engine", Kind: "class", Lang: "python"},
		},
		"tests/test_x.py": {
			{RelFile: "tests/test_x.py", Line: 1, Name: "TestX", Kind: "class", Lang: "python"},
		},
		"docs/x.md": {
			{RelFile: "docs/x.md", Line: 1, Name: "X", Kind: "heading-1", Lang: "markdown"},
		},
	}
	ranks := map[string]float32{
		"src/engine.py":   0,
		"tests/test_x.py": 0,
		"docs/x.md":       0,
	}
	return tags, ranks, Build(tags, nil)
}

func TestFindDeadCodeHidesTestAndDocOrphansByDefault(t *testing.T) {
	tags, ranks, g := orphanFixtures()
	res := FindDeadCode(g, tags, ranks, DeadCodeOptions{MinRank: 0.001})

	if len(res.OrphanFiles) != 1 {
		t.Fatalf("expected exactly 1 orphan (test/docs hidden by default), got %d: %+v", len(res.OrphanFiles), res.OrphanFiles)
	}
	o := res.OrphanFiles[0]
	if o.File != "src/engine.py" {
		t.Fatalf("expected src/engine.py to be the only orphan, got %q", o.File)
	}
	if o.Lang != "python" {
		t.Errorf("orphan lang = %q, want python", o.Lang)
	}
	if o.Category != "source" {
		t.Errorf("orphan category = %q, want source", o.Category)
	}
	if !strings.Contains(res.Summary, "hidden") {
		t.Errorf("summary should mention hidden orphans, got %q", res.Summary)
	}
}

func TestFindDeadCodeIncludeFlags(t *testing.T) {
	tags, ranks, g := orphanFixtures()
	res := FindDeadCode(g, tags, ranks, DeadCodeOptions{
		MinRank:            0.001,
		IncludeTestOrphans: true,
		IncludeDocOrphans:  true,
	})

	if len(res.OrphanFiles) != 3 {
		t.Fatalf("expected all 3 orphans with include flags, got %d: %+v", len(res.OrphanFiles), res.OrphanFiles)
	}
	got := map[string]OrphanFile{}
	for _, o := range res.OrphanFiles {
		got[o.File] = o
	}
	checks := map[string]struct{ lang, category string }{
		"src/engine.py":   {"python", "source"},
		"tests/test_x.py": {"python", "test"},
		"docs/x.md":       {"markdown", "docs"},
	}
	for file, wc := range checks {
		o, ok := got[file]
		if !ok {
			t.Fatalf("missing orphan %q in %+v", file, res.OrphanFiles)
		}
		if o.Lang != wc.lang {
			t.Errorf("%s lang = %q, want %q", file, o.Lang, wc.lang)
		}
		if o.Category != wc.category {
			t.Errorf("%s category = %q, want %q", file, o.Category, wc.category)
		}
	}
	if strings.Contains(res.Summary, "hidden") {
		t.Errorf("summary must not mention hidden orphans when all included, got %q", res.Summary)
	}
}

func TestFindDeadCodeSymbolFiltersThroughOptions(t *testing.T) {
	tags := map[string][]parser.Tag{
		"src/mod.py": {
			{RelFile: "src/mod.py", Line: 1, Name: "_helper", Kind: "function", Lang: "python"},
			{RelFile: "src/mod.py", Line: 5, Name: "helper", Kind: "function", Lang: "python"},
		},
	}
	ranks := map[string]float32{"src/mod.py": 0}
	g := Build(tags, nil)

	// UnexportedOnly: _helper (unexported) reported, helper (exported) not.
	res := FindDeadCode(g, tags, ranks, DeadCodeOptions{MinRank: 0.001, UnexportedOnly: true})
	names := map[string]bool{}
	for _, d := range res.DeadSymbols {
		names[d.Name] = true
	}
	if !names["_helper"] {
		t.Errorf("UnexportedOnly: expected _helper to be reported, got %v", res.DeadSymbols)
	}
	if names["helper"] {
		t.Errorf("UnexportedOnly: exported helper must not be reported, got %v", res.DeadSymbols)
	}

	// No visibility filter: both unreferenced defs reported.
	res = FindDeadCode(g, tags, ranks, DeadCodeOptions{MinRank: 0.001})
	if len(res.DeadSymbols) != 2 {
		t.Fatalf("no filter: expected both defs reported, got %v", res.DeadSymbols)
	}
}
