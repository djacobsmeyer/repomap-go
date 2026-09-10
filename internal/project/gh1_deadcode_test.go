package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/djacobsmeyer/repomap-go/internal/events"
	"github.com/djacobsmeyer/repomap-go/internal/graph"
)

// TestGH1PythonDeadCodeEndToEnd is the end-to-end regression for GitHub
// issue #1: find_dead_code(unexported_only: true) on a real Python repo had
// ~90% false positives in three classes:
//
//   - A: function-local assignments reported as dead module-level variables
//   - B: functions referenced only in value positions (callbacks, dict
//     values, attribute assignments, decorators, defaults) reported dead
//   - C: orphan_files listing test/docs files that have no importers by
//     design
//
// The fixture at testdata/gh1_python reproduces all three classes at once.
func TestGH1PythonDeadCodeEndToEnd(t *testing.T) {
	root := copyGH1Fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p, err := New(root, events.NewBus())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		if err := p.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}()

	// Sanity: the fixture must actually be indexed (guards against a silent
	// empty index making every assertion below pass for the wrong reason).
	if n := p.TagCount(); n == 0 {
		t.Fatal("index is empty; fixture was not parsed")
	}

	// MinRank: 1.0 — in a tiny fixture every file's PageRank is well below 1,
	// so every zero-inbound file qualifies as an orphan candidate.
	opts := graph.DeadCodeOptions{MinRank: 1.0, UnexportedOnly: true}
	res := p.FindDeadCode(opts)

	dead := map[string]graph.DeadSymbol{}
	for _, d := range res.DeadSymbols {
		dead[d.Name] = d
	}

	// The dead set must be EXACTLY the two true positives — nothing else.
	if len(dead) != 2 {
		t.Fatalf("dead symbols = %v (names %v), want exactly [_UNUSED_LIMIT _legacy_extract_media]", dead, names(dead))
	}
	for _, want := range []string{"_UNUSED_LIMIT", "_legacy_extract_media"} {
		if _, ok := dead[want]; !ok {
			t.Errorf("dead symbols missing true positive %q; got %v", want, names(dead))
		}
	}

	// False positives from the issue — none may appear.
	for _, fp := range []string{
		"_t0", "_dt", "_", // class A: function locals
		"_propagate",                          // class B: callback argument
		"_qwen35_extract_queries",             // class B: dict value
		"_patched_step",                       // class B: attribute assignment
		"_CACHE_PERSIST_VERSION",              // read via cache_header()
		"_with_retry", "_fetch", "_time_step", // class B: decorator / plain call
		"_install_stepper", "_EXTRACTOR_REGISTRY",
	} {
		if _, ok := dead[fp]; ok {
			t.Errorf("dead symbols contain false positive %q (GH-1); got %v", fp, names(dead))
		}
	}

	// Class C: test and docs orphans are hidden by default.
	for _, f := range res.OrphanFiles {
		if f.File == "tests/test_engine.py" || f.File == "docs/design.md" {
			t.Errorf("orphan_files contains %q but it must be hidden by default (category %q)", f.File, f.Category)
		}
	}

	// With the include flags, they appear with the right category.
	resAll := p.FindDeadCode(graph.DeadCodeOptions{
		MinRank:            1.0,
		UnexportedOnly:     true,
		IncludeTestOrphans: true,
		IncludeDocOrphans:  true,
	})
	cats := map[string]string{}
	for _, f := range resAll.OrphanFiles {
		cats[f.File] = f.Category
	}
	if c := cats["tests/test_engine.py"]; c != "test" {
		t.Errorf("tests/test_engine.py orphan category = %q, want %q (all: %v)", c, "test", cats)
	}
	if c := cats["docs/design.md"]; c != "docs" {
		t.Errorf("docs/design.md orphan category = %q, want %q (all: %v)", c, "docs", cats)
	}
}

// copyGH1Fixture copies the gh1_python testdata tree into a temp dir so the
// Project's cache (.repomap/) never pollutes the checked-in fixture.
func copyGH1Fixture(t *testing.T) string {
	t.Helper()
	src := filepath.Join("testdata", "gh1_python")
	dst := t.TempDir()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

func names(m map[string]graph.DeadSymbol) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
