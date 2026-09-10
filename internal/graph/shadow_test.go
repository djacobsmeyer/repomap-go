package graph

import (
	"testing"

	"github.com/djacobsmeyer/repomap-go/internal/parser"
)

// pyTag builds a python tag for fixture wiring.
func pyTag(file, name, kind string) parser.Tag {
	return parser.Tag{RelFile: file, Line: 1, Name: name, Kind: kind, Lang: "python"}
}

// edgeExists reports whether g has a refFile -> defFile edge.
func edgeExists(g *FileGraph, refFile, defFile string) bool {
	if g.Edges[refFile] == nil {
		return false
	}
	_, ok := g.Edges[refFile][defFile]
	return ok
}

// TestShadowLocalDefNoEdge: three modules each define AND reference `log`.
// The local-shadowing rule resolves each ref to its own module-level def, so
// Build must yield zero edges (previously N modules produced N×N spurious
// edges).
func TestShadowLocalDefNoEdge(t *testing.T) {
	tags := map[string][]parser.Tag{
		"a.py": {pyTag("a.py", "log", "variable"), pyTag("a.py", "log", "ref")},
		"b.py": {pyTag("b.py", "log", "variable"), pyTag("b.py", "log", "ref")},
		"c.py": {pyTag("c.py", "log", "variable"), pyTag("c.py", "log", "ref")},
	}
	g := Build(tags, nil)
	for _, src := range g.Files {
		for _, dst := range g.Files {
			if edgeExists(g, src, dst) {
				t.Errorf("edge %s -> %s exists; self-defined name must shadow locally", src, dst)
			}
		}
	}
}

// TestShadowNoLocalDefStillEdges: b.py references `_helper` which only a.py
// defines — the cross-file edge must still exist.
func TestShadowNoLocalDefStillEdges(t *testing.T) {
	tags := map[string][]parser.Tag{
		"a.py": {pyTag("a.py", "_helper", "function")},
		"b.py": {pyTag("b.py", "_helper", "ref")},
	}
	g := Build(tags, nil)
	if !edgeExists(g, "b.py", "a.py") {
		t.Fatalf("missing edge b.py -> a.py for unshadowed ref; edges: %+v", g.Edges)
	}
}

// TestShadowPerNameNotPerFile: c.py defines `log` (shadowed) but also refs
// `helper2` defined only in d.py — the rule applies per name, so the
// c.py -> d.py edge must exist.
func TestShadowPerNameNotPerFile(t *testing.T) {
	tags := map[string][]parser.Tag{
		"c.py": {
			pyTag("c.py", "log", "variable"),
			pyTag("c.py", "log", "ref"),
			pyTag("c.py", "helper2", "ref"),
		},
		"d.py": {pyTag("d.py", "helper2", "function")},
	}
	g := Build(tags, nil)
	if !edgeExists(g, "c.py", "d.py") {
		t.Fatalf("missing edge c.py -> d.py; shadowing is per-name, not per-file; edges: %+v", g.Edges)
	}
	if edgeExists(g, "d.py", "c.py") {
		t.Errorf("unexpected edge d.py -> c.py; edges: %+v", g.Edges)
	}
}
