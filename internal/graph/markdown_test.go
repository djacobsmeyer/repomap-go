package graph

import (
	"testing"

	"github.com/djacobsmeyer/repomap-go/internal/mdresolver"
	"github.com/djacobsmeyer/repomap-go/internal/parser"
)

// mdResolverFor returns a resolveRef closure bound to the given file list.
func mdResolverFor(files []string) func(string, string) string {
	return func(dest, src string) string { return mdresolver.Resolve(dest, src, files) }
}

func hasEdge(g *FileGraph, from, to string) bool {
	if g.Edges[from] == nil {
		return false
	}
	_, ok := g.Edges[from][to]
	return ok
}

// A markdown link between two documents must create a file -> file edge.
func TestMarkdownLinkCreatesEdge(t *testing.T) {
	tags := map[string][]parser.Tag{
		"a.md": {
			{RelFile: "a.md", Line: 1, Name: "Doc A", Kind: "heading-1", Lang: "markdown"},
			{RelFile: "a.md", Line: 3, Name: "./b.md", Kind: "ref", Lang: "markdown"},
		},
		"b.md": {
			{RelFile: "b.md", Line: 1, Name: "Doc B", Kind: "heading-1", Lang: "markdown"},
		},
	}
	g := Build(tags, mdResolverFor([]string{"a.md", "b.md"}))
	if !hasEdge(g, "a.md", "b.md") {
		t.Fatalf("expected edge a.md -> b.md, edges=%v", g.Edges)
	}
}

// External URLs must never create graph edges.
func TestMarkdownExternalURLNoEdge(t *testing.T) {
	tags := map[string][]parser.Tag{
		"a.md": {
			{RelFile: "a.md", Line: 3, Name: "https://example.com", Kind: "ref", Lang: "markdown"},
		},
		"b.md": {
			{RelFile: "b.md", Line: 1, Name: "Doc B", Kind: "heading-1", Lang: "markdown"},
		},
	}
	g := Build(tags, mdResolverFor([]string{"a.md", "b.md"}))
	if len(g.Edges) != 0 {
		t.Fatalf("external URL must not create edges, edges=%v", g.Edges)
	}
}

// Image embed destinations must never create graph edges.
func TestMarkdownImageNoEdge(t *testing.T) {
	tags := map[string][]parser.Tag{
		"a.md": {
			// Even if an image dest leaked through as a ref, the resolver
			// filters it out before edges are built.
			{RelFile: "a.md", Line: 3, Name: "diagram.png", Kind: "ref", Lang: "markdown"},
		},
		"diagram.png": {},
	}
	g := Build(tags, mdResolverFor([]string{"a.md", "diagram.png"}))
	if len(g.Edges) != 0 {
		t.Fatalf("image embed must not create edges, edges=%v", g.Edges)
	}
}

// Two files with an identically-named heading must NOT create a false
// cross-file dependency — heading defs are file-namespaced.
func TestMarkdownHeadingCollisionNoFalseEdge(t *testing.T) {
	tags := map[string][]parser.Tag{
		"a.md": {
			{RelFile: "a.md", Line: 1, Name: "Introduction", Kind: "heading-1", Lang: "markdown"},
		},
		"b.md": {
			{RelFile: "b.md", Line: 1, Name: "Introduction", Kind: "heading-1", Lang: "markdown"},
		},
	}
	g := Build(tags, mdResolverFor([]string{"a.md", "b.md"}))
	if len(g.Edges) != 0 {
		t.Fatalf("identical heading names must not create cross-file edges, edges=%v", g.Edges)
	}
}

// A wikilink must resolve to the target file and create an edge.
func TestMarkdownWikilinkCreatesEdge(t *testing.T) {
	tags := map[string][]parser.Tag{
		"notes/a.md": {
			{RelFile: "notes/a.md", Line: 3, Name: "Architecture", Kind: "ref", Lang: "markdown"},
		},
		"docs/Architecture.md": {
			{RelFile: "docs/Architecture.md", Line: 1, Name: "Architecture", Kind: "heading-1", Lang: "markdown"},
		},
	}
	g := Build(tags, mdResolverFor([]string{"notes/a.md", "docs/Architecture.md"}))
	if !hasEdge(g, "notes/a.md", "docs/Architecture.md") {
		t.Fatalf("expected edge notes/a.md -> docs/Architecture.md, edges=%v", g.Edges)
	}
}

// A nil resolver must not panic and must leave non-markdown behavior intact.
func TestBuildNilResolverBackwardCompatible(t *testing.T) {
	tags := map[string][]parser.Tag{
		"a.go": {
			{RelFile: "a.go", Line: 1, Name: "Foo", Kind: "function", Lang: "go"},
		},
		"b.go": {
			{RelFile: "b.go", Line: 5, Name: "Foo", Kind: "ref", Lang: "go"},
		},
	}
	g := Build(tags, nil)
	if !hasEdge(g, "b.go", "a.go") {
		t.Fatalf("expected go edge b.go -> a.go with nil resolver, edges=%v", g.Edges)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Architecture Overview": "architecture-overview",
		"Hello, World!":         "hello-world",
		"API v2.0":              "api-v20",
		// GitHub-style slugify maps every space to a dash, including
		// leading/trailing whitespace.
		"  Trimmed  ": "--trimmed--",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
