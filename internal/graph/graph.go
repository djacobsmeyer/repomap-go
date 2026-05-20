package graph

import (
	"fmt"
	"sort"
	"strings"

	"github.com/djacobsmeyer/repomap-go/internal/parser"
)

// FileGraph models references between files via shared symbol names.
// Edges[a][b] = weight of an edge from a -> b (a references b).
type FileGraph struct {
	Edges         map[string]map[string]float32
	InvertedEdges map[string]map[string]float32
	Files         []string
}

// slugify converts a heading string to a GitHub-style anchor slug: lowercase,
// spaces to dashes, and every other non-alphanumeric-dash rune dropped.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if r == ' ' {
			return '-'
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return -1
	}, s)
	return s
}

// symbolKey returns the bucket key for a tag. For markdown heading definitions
// the key is namespaced by file (RelFile#slug) so that a common heading name
// like "Introduction" in two documents does not create a false cross-file
// dependency. All other tags key on the bare name.
func symbolKey(t parser.Tag) string {
	if t.Lang == "markdown" && t.IsDef() {
		return t.RelFile + "#" + slugify(t.Name)
	}
	return t.Name
}

// Build constructs a file graph from a per-file tag index.
// For each symbol name, files that contain "ref" tags get edges pointing to
// files that contain "def" tags for that name.
//
// resolveRef, if non-nil, is invoked for every markdown "ref" tag to normalize
// its raw link destination (Name) into a canonical RelFile before bucketing.
// Returning "" from resolveRef drops the reference (external URLs, image
// embeds, unresolvable targets) so it creates no edge. If resolveRef is nil,
// markdown refs are treated like any other ref (identity) — keeping non-
// markdown callers and tests backward compatible.
func Build(tagsByFile map[string][]parser.Tag, resolveRef func(name, relFile string) string) *FileGraph {
	g := &FileGraph{
		Edges:         make(map[string]map[string]float32),
		InvertedEdges: make(map[string]map[string]float32),
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
	getBucket := func(key string) *symBucket {
		b, ok := buckets[key]
		if !ok {
			b = &symBucket{defs: map[string]struct{}{}, refs: map[string]struct{}{}}
			buckets[key] = b
		}
		return b
	}

	// mdFiles tracks every file that contains markdown tags. Each markdown
	// file is registered as a definer of a symbol equal to its own RelFile so
	// that a resolved inter-document link (which buckets on the target
	// RelFile) finds a matching def and produces a file -> file edge.
	mdFiles := map[string]struct{}{}

	for file, tags := range tagsByFile {
		for _, t := range tags {
			if t.Lang == "markdown" {
				mdFiles[file] = struct{}{}
			}
			// Markdown references hold a raw link destination. Resolve it to
			// a canonical RelFile so the edge points at the right document.
			if t.Lang == "markdown" && t.Kind == "ref" {
				dest := t.Name
				if resolveRef != nil {
					dest = resolveRef(t.Name, t.RelFile)
				}
				if dest == "" {
					// External URL, image embed, or unresolvable — no edge.
					continue
				}
				// Bucket the ref on the bare target RelFile; the target
				// file's synthetic def (added below) lives in the same key.
				getBucket(dest).refs[file] = struct{}{}
				continue
			}
			// Non-markdown tags (and markdown heading defs) bucket on
			// symbolKey, preserving the def/ref distinction.
			b := getBucket(symbolKey(t))
			if t.Kind == "ref" {
				b.refs[file] = struct{}{}
			} else {
				b.defs[file] = struct{}{}
			}
		}
	}

	// Register each markdown file as a definer of its own RelFile symbol.
	for file := range mdFiles {
		getBucket(file).defs[file] = struct{}{}
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
				if g.InvertedEdges[defFile] == nil {
					g.InvertedEdges[defFile] = make(map[string]float32)
				}
				g.InvertedEdges[defFile][refFile] += 1.0
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
			if t.IsDef() {
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

// --- Phase 2: blast radius ---------------------------------------------------

type BlastRadiusResult struct {
	Symbol               string            `json:"symbol"`
	DefinedIn            string            `json:"defined_in"`
	DirectDependents     []DependentSymbol `json:"direct_dependents"`
	TransitiveDependents []DependentSymbol `json:"transitive_dependents"`
	TotalFilesAffected   int               `json:"total_files_affected"`
}

type DependentSymbol struct {
	File   string `json:"file"`
	Symbol string `json:"symbol"`
	Line   int    `json:"line"`
	Depth  int    `json:"depth"`
}

// BlastRadius returns every file/symbol that transitively depends on `symbol`.
// If `file` is non-empty, only definitions in that file seed the traversal.
func BlastRadius(g *FileGraph, tagsByFile map[string][]parser.Tag, symbol, file string, maxDepth int) BlastRadiusResult {
	if maxDepth <= 0 {
		maxDepth = 3
	}
	res := BlastRadiusResult{
		Symbol:               symbol,
		DirectDependents:     []DependentSymbol{},
		TransitiveDependents: []DependentSymbol{},
	}

	// Find seed def tags.
	var seedFiles []string
	seedSet := map[string]struct{}{}
	var firstDef *parser.Tag
	for f, tags := range tagsByFile {
		if file != "" && f != file {
			continue
		}
		for i, t := range tags {
			if t.Name == symbol && t.IsDef() {
				if _, ok := seedSet[f]; !ok {
					seedSet[f] = struct{}{}
					seedFiles = append(seedFiles, f)
				}
				if firstDef == nil {
					firstDef = &tags[i]
				}
			}
		}
	}
	if firstDef != nil {
		res.DefinedIn = fmt.Sprintf("%s:%d", firstDef.RelFile, firstDef.Line)
	}
	if len(seedFiles) == 0 || g == nil {
		return res
	}

	// BFS via InvertedEdges. Track depth per file (shortest path).
	depth := map[string]int{}
	for _, f := range seedFiles {
		depth[f] = 0
	}
	queue := append([]string{}, seedFiles...)
	touched := map[string]struct{}{}
	for _, f := range seedFiles {
		touched[f] = struct{}{}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		d := depth[cur]
		if d >= maxDepth {
			continue
		}
		for next := range g.InvertedEdges[cur] {
			if _, seen := depth[next]; seen {
				continue
			}
			depth[next] = d + 1
			touched[next] = struct{}{}
			queue = append(queue, next)
		}
	}

	// For each touched file (excluding seeds), collect ref-tags matching symbol.
	type entry struct {
		dep DependentSymbol
	}
	var direct, transitive []DependentSymbol
	files := make([]string, 0, len(depth))
	for f := range depth {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		if _, isSeed := seedSet[f]; isSeed {
			continue
		}
		d := depth[f]
		matched := false
		for _, t := range tagsByFile[f] {
			if t.Name == symbol && t.Kind == "ref" {
				ds := DependentSymbol{File: f, Symbol: t.Name, Line: t.Line, Depth: d}
				if d == 1 {
					direct = append(direct, ds)
				} else {
					transitive = append(transitive, ds)
				}
				matched = true
			}
		}
		// Even if no specific ref-tag matched the symbol name, the file is in
		// the blast-radius (transitively referenced through another path).
		if !matched {
			ds := DependentSymbol{File: f, Symbol: "", Line: 0, Depth: d}
			if d == 1 {
				direct = append(direct, ds)
			} else {
				transitive = append(transitive, ds)
			}
		}
	}
	res.DirectDependents = direct
	res.TransitiveDependents = transitive
	if res.DirectDependents == nil {
		res.DirectDependents = []DependentSymbol{}
	}
	if res.TransitiveDependents == nil {
		res.TransitiveDependents = []DependentSymbol{}
	}
	// Unique files affected (excluding seeds).
	uniq := map[string]struct{}{}
	for f := range touched {
		if _, isSeed := seedSet[f]; isSeed {
			continue
		}
		uniq[f] = struct{}{}
	}
	res.TotalFilesAffected = len(uniq)
	return res
}

// --- Phase 2: dead code ------------------------------------------------------

type DeadCodeResult struct {
	DeadSymbols []DeadSymbol `json:"dead_symbols"`
	OrphanFiles []OrphanFile `json:"orphan_files"`
	Summary     string       `json:"summary"`
}

type DeadSymbol struct {
	File string `json:"file"`
	Name string `json:"name"`
	Line int    `json:"line"`
	Kind string `json:"kind"`
}

type OrphanFile struct {
	File string  `json:"file"`
	Rank float32 `json:"rank"`
}

// isUnexported reports whether `name` follows the language's convention for
// an unexported / private symbol. Returns false for languages without an
// enforced visibility convention (so the filter is effectively skipped).
func isUnexported(name, lang string) bool {
	if name == "" {
		return false
	}
	switch lang {
	case "go":
		c := name[0]
		return c >= 'a' && c <= 'z'
	case "python":
		// Leading _ but NOT dunder (__name__ etc, called by framework).
		return strings.HasPrefix(name, "_") && !strings.HasPrefix(name, "__")
	case "typescript", "javascript":
		// No enforced convention — treat leading _ as unexported by convention.
		return strings.HasPrefix(name, "_")
	}
	return false
}

// FindDeadCode returns symbols defined but never referenced, plus files with
// no inbound edges and PageRank at or below minRank.
//
// Filters:
//   - unexportedOnly: keep only symbols whose name looks unexported in their
//     language (best signal-to-noise — externally-called code is invisible).
//   - exportedOnly:   keep only symbols that don't look unexported. Mutually
//     exclusive with unexportedOnly; if both are true, no symbols are kept.
//   - kinds: if non-empty, keep only symbols whose Kind is in this slice.
func FindDeadCode(
	g *FileGraph,
	tagsByFile map[string][]parser.Tag,
	ranks map[string]float32,
	minRank float32,
	unexportedOnly bool,
	exportedOnly bool,
	kinds []string,
) DeadCodeResult {
	referenced := map[string]struct{}{}
	for _, tags := range tagsByFile {
		for _, t := range tags {
			if t.Kind == "ref" {
				referenced[t.Name] = struct{}{}
			}
		}
	}

	kindSet := map[string]struct{}{}
	for _, k := range kinds {
		if k != "" {
			kindSet[k] = struct{}{}
		}
	}
	mutualExclusion := unexportedOnly && exportedOnly

	var dead []DeadSymbol
	files := make([]string, 0, len(tagsByFile))
	for f := range tagsByFile {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		tags := tagsByFile[f]
		// Stable order by line.
		ordered := make([]parser.Tag, len(tags))
		copy(ordered, tags)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].Line < ordered[j].Line })
		for _, t := range ordered {
			if !t.IsDef() {
				continue
			}
			if _, ok := referenced[t.Name]; ok {
				continue
			}
			if len(kindSet) > 0 {
				if _, ok := kindSet[t.Kind]; !ok {
					continue
				}
			}
			if mutualExclusion {
				// Both flags set — silently include nothing.
				continue
			}
			if unexportedOnly && !isUnexported(t.Name, t.Lang) {
				continue
			}
			if exportedOnly && isUnexported(t.Name, t.Lang) {
				continue
			}
			dead = append(dead, DeadSymbol{File: f, Name: t.Name, Line: t.Line, Kind: t.Kind})
		}
	}

	var orphans []OrphanFile
	if g != nil {
		for _, f := range g.Files {
			if len(g.InvertedEdges[f]) > 0 {
				continue
			}
			r := ranks[f]
			if r > minRank {
				continue
			}
			orphans = append(orphans, OrphanFile{File: f, Rank: r})
		}
		sort.Slice(orphans, func(i, j int) bool { return orphans[i].File < orphans[j].File })
	}

	if dead == nil {
		dead = []DeadSymbol{}
	}
	if orphans == nil {
		orphans = []OrphanFile{}
	}
	return DeadCodeResult{
		DeadSymbols: dead,
		OrphanFiles: orphans,
		Summary:     fmt.Sprintf("%d dead symbols, %d orphan files", len(dead), len(orphans)),
	}
}
