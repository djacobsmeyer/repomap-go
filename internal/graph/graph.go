package graph

import (
	"fmt"
	"sort"
	"strings"

	"github.com/yourusername/repomap-go/internal/parser"
)

// FileGraph models references between files via shared symbol names.
// Edges[a][b] = weight of an edge from a -> b (a references b).
type FileGraph struct {
	Edges map[string]map[string]float32
	Files []string
}

// Build constructs a file graph from a per-file tag index.
// For each symbol name, files that contain "ref" tags get edges pointing to
// files that contain "def" tags for that name.
func Build(tagsByFile map[string][]parser.Tag) *FileGraph {
	g := &FileGraph{
		Edges: make(map[string]map[string]float32),
	}
	// Collect file list (stable order).
	for f := range tagsByFile {
		g.Files = append(g.Files, f)
	}
	sort.Strings(g.Files)

	// Group: symbol name -> {definers: set[file], referencers: set[file]}
	type symBucket struct {
		defs map[string]struct{}
		refs map[string]struct{}
	}
	buckets := make(map[string]*symBucket)
	for file, tags := range tagsByFile {
		for _, t := range tags {
			b, ok := buckets[t.Name]
			if !ok {
				b = &symBucket{defs: map[string]struct{}{}, refs: map[string]struct{}{}}
				buckets[t.Name] = b
			}
			if t.Kind == "def" {
				b.defs[file] = struct{}{}
			} else if t.Kind == "ref" {
				b.refs[file] = struct{}{}
			}
		}
	}

	for _, b := range buckets {
		if len(b.defs) == 0 || len(b.refs) == 0 {
			continue
		}
		for refFile := range b.refs {
			for defFile := range b.defs {
				if refFile == defFile {
					continue
				}
				if g.Edges[refFile] == nil {
					g.Edges[refFile] = make(map[string]float32)
				}
				g.Edges[refFile][defFile] += 1.0
			}
		}
	}
	return g
}

// PageRank computes float32 PageRank values for each file in the graph.
func PageRank(g *FileGraph, iterations int, damping float32) map[string]float32 {
	if iterations <= 0 {
		iterations = 15
	}
	if damping <= 0 {
		damping = 0.85
	}

	files := g.Files
	n := len(files)
	if n == 0 {
		return map[string]float32{}
	}

	rank := make(map[string]float32, n)
	initial := float32(1.0) / float32(n)
	for _, f := range files {
		rank[f] = initial
	}

	// Precompute outgoing weight sums per node.
	outSum := make(map[string]float32, n)
	for src, edges := range g.Edges {
		var s float32
		for _, w := range edges {
			s += w
		}
		outSum[src] = s
	}

	// Reverse adjacency: dst -> []{src, weight}
	type inEdge struct {
		src string
		w   float32
	}
	inAdj := make(map[string][]inEdge, n)
	for src, edges := range g.Edges {
		for dst, w := range edges {
			inAdj[dst] = append(inAdj[dst], inEdge{src: src, w: w})
		}
	}

	base := (1.0 - damping) / float32(n)

	for iter := 0; iter < iterations; iter++ {
		newRank := make(map[string]float32, n)
		var danglingMass float32
		for _, f := range files {
			if outSum[f] == 0 {
				danglingMass += rank[f]
			}
		}
		danglingContribution := damping * danglingMass / float32(n)

		for _, f := range files {
			var sum float32
			for _, e := range inAdj[f] {
				if outSum[e.src] > 0 {
					sum += rank[e.src] * (e.w / outSum[e.src])
				}
			}
			newRank[f] = base + danglingContribution + damping*sum
		}
		rank = newRank
	}

	// Normalize so values sum to 1.
	var total float32
	for _, v := range rank {
		total += v
	}
	if total > 0 {
		for k, v := range rank {
			rank[k] = v / total
		}
	}
	return rank
}

// RenderMap renders the top-ranked files (within the token budget) into a
// human-readable map. Only "def" tags are emitted.
func RenderMap(ranks map[string]float32, tagsByFile map[string][]parser.Tag, tokenBudget int) string {
	if tokenBudget <= 0 {
		tokenBudget = 8192
	}
	type fr struct {
		file string
		rank float32
	}
	sorted := make([]fr, 0, len(ranks))
	for f, r := range ranks {
		sorted = append(sorted, fr{file: f, rank: r})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].rank == sorted[j].rank {
			return sorted[i].file < sorted[j].file
		}
		return sorted[i].rank > sorted[j].rank
	})

	var b strings.Builder
	approxTokens := func() int { return b.Len() / 4 }

	for _, entry := range sorted {
		tags := tagsByFile[entry.file]
		// Filter to defs only, stable line order.
		var defs []parser.Tag
		for _, t := range tags {
			if t.Kind == "def" {
				defs = append(defs, t)
			}
		}
		if len(defs) == 0 {
			continue
		}
		sort.Slice(defs, func(i, j int) bool { return defs[i].Line < defs[j].Line })

		// Probe write into a temp section, then check budget.
		var section strings.Builder
		fmt.Fprintf(&section, "%s:\n", entry.file)
		fmt.Fprintf(&section, "(Rank: %.2f)\n", entry.rank*100)
		for _, t := range defs {
			fmt.Fprintf(&section, "  %d: %s\n", t.Line, t.Name)
		}
		section.WriteString("⋮...\n")

		// Check if appending exceeds budget.
		projected := (b.Len() + section.Len()) / 4
		if projected > tokenBudget && b.Len() > 0 {
			break
		}
		b.WriteString(section.String())
		if approxTokens() > tokenBudget {
			break
		}
	}
	return b.String()
}
