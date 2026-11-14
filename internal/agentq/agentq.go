// Package agentq answers code-navigation questions for small-context coding
// agents: compact plain-text results, each within a token budget, that say
// when they are truncated and carry enough (file:line, the enclosing
// function and its line range, the line itself) for the agent to act
// without reading whole files.
//
// It works on a snapshot of a project's index (see Index) and never touches
// the filesystem except through Index.Read.
package agentq

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/djacobsmeyer/repomap-go/internal/graph"
	"github.com/djacobsmeyer/repomap-go/internal/parser"
)

// Index is the read-only view of one project the queries run on.
type Index struct {
	// Tags is the tag index: rel path -> tags of a parsed file.
	Tags map[string][]parser.Tag
	// Ranks is the PageRank of each parsed file (may be nil).
	Ranks map[string]float32
	// Files is every regular, non-ignored file (rel path -> size), parsed
	// or not. Files that are not in Tags are grepped as plain text.
	Files map[string]int64
	// Read returns a project file's contents (nil = no line text).
	Read func(rel string) ([]byte, error)
}

// Defaults and caps for the budget parameters.
const (
	DefaultCallersTokens = 1500
	// DefaultCallersDeepTokens is the default at depth 2+: the A/B t2 answer
	// (8 sites, 17 second-hop sites) is ~1.3k real tokens.
	DefaultCallersDeepTokens = 2000
	DefaultOutlineTokens     = 1200
	DefaultSearchTokens      = 600
	MaxTokensCap             = 8000
	maxLineChars             = 160
	// textGrepMaxBytes caps the size of an unparsed file that is grepped.
	textGrepMaxBytes = 1 << 20
)

// EstimateTokens is the token estimate the budgets use: one token per
// digit (Qwen and Llama-3 style tokenizers split numbers into single digits
// or short groups, and line numbers are a large share of these answers)
// plus one per 3.2 other bytes. Measured against the Qwen3.8 tokenizer on
// callers/outline/search output it is within about 10%, mostly above.
func EstimateTokens(s string) int {
	digits := 0
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			digits++
		}
	}
	return digits + ((len(s)-digits)*10+31)/32
}

func clampBudget(v, def int) int {
	if v <= 0 {
		return def
	}
	if v > MaxTokensCap {
		return MaxTokensCap
	}
	return v
}

// IsTestFile reports whether rel is a test file by repomap's naming rules.
func IsTestFile(rel string) bool {
	_, cat := graph.ClassifyFile(rel)
	return cat == "test"
}

// writer accumulates output against a token budget.
type writer struct {
	b      strings.Builder
	max    int
	tokens int
}

func newWriter(maxTokens int) *writer { return &writer{max: maxTokens} }

// fits reports whether s, plus reserve more tokens, still fits the budget.
func (w *writer) fits(s string, reserve int) bool {
	return w.tokens+EstimateTokens(s)+reserve <= w.max
}
func (w *writer) add(s string) {
	w.b.WriteString(s)
	w.tokens += EstimateTokens(s)
}
func (w *writer) String() string { return w.b.String() }

// fileLines caches split file contents for line lookups.
type fileLines struct {
	read  func(string) ([]byte, error)
	cache map[string][]string
}

func newFileLines(read func(string) ([]byte, error)) *fileLines {
	return &fileLines{read: read, cache: map[string][]string{}}
}

func (fl *fileLines) lines(rel string) []string {
	if l, ok := fl.cache[rel]; ok {
		return l
	}
	var out []string
	if fl.read != nil {
		if data, err := fl.read(rel); err == nil {
			out = strings.Split(string(data), "\n")
		}
	}
	fl.cache[rel] = out
	return out
}

// line returns line n (1-based), trimmed and capped, or "".
func (fl *fileLines) line(rel string, n int) string {
	l := fl.lines(rel)
	if n < 1 || n > len(l) {
		return ""
	}
	return clip(strings.TrimSpace(l[n-1]))
}

func (fl *fileLines) count(rel string) int {
	l := fl.lines(rel)
	if len(l) > 0 && l[len(l)-1] == "" {
		return len(l) - 1
	}
	return len(l)
}

func clip(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	if len(s) > maxLineChars {
		return s[:maxLineChars] + "…"
	}
	return s
}

// scopeKinds are the definition kinds that can enclose a reference.
var scopeKinds = map[string]bool{"function": true, "method": true, "class": true}

// enclosing returns the innermost function/method/class definition in tags
// whose span contains line, and the innermost class around that one (for
// "Class.method" names). ok is false at module level.
func enclosing(tags []parser.Tag, line int) (fn parser.Tag, qual string, ok bool) {
	var best *parser.Tag
	for i := range tags {
		t := &tags[i]
		if !scopeKinds[t.Kind] || t.Line > line || t.EndLine < line {
			continue
		}
		if best == nil || t.Line > best.Line || (t.Line == best.Line && t.EndLine < best.EndLine) {
			best = t
		}
	}
	if best == nil {
		return parser.Tag{}, "", false
	}
	qual = best.Name
	if best.Kind != "class" {
		var cls *parser.Tag
		for i := range tags {
			t := &tags[i]
			if t.Kind != "class" || t.Line > best.Line || t.EndLine < best.EndLine || (t.Line == best.Line && t.EndLine == best.EndLine) {
				continue
			}
			if cls == nil || t.Line > cls.Line {
				cls = t
			}
		}
		if cls != nil {
			qual = cls.Name + "." + best.Name
		}
	}
	return *best, qual, true
}

func span(t parser.Tag) string {
	if t.EndLine > t.Line {
		return fmt.Sprintf("%d-%d", t.Line, t.EndLine)
	}
	return fmt.Sprintf("%d", t.Line)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- callers -----------------------------------------------------------------

// CallersOptions configure Callers.
type CallersOptions struct {
	Symbol string
	// File narrows the definition to one file when the name is defined in
	// several; references in files that define their own copy are dropped.
	File string
	// Depth 1 lists references to Symbol; 2 and 3 add references to the
	// functions that contain them (impact of a change), by name.
	Depth int
	// Context adds this many lines before and after each reference (0-3).
	Context      int
	IncludeTests bool
	// Path keeps only references under this file or directory.
	Path      string
	MaxTokens int
}

type site struct {
	file  string
	line  int
	scope string // qualified enclosing name ("" = module level)
	fn    parser.Tag
	hasFn bool
}

// resolver attributes references of one name to one definition (origin)
// when the name is defined in several files. It is a by-name heuristic, not
// type resolution:
//   - a reference in another file that defines the same name belongs to
//     that file's own definition (module-level shadowing);
//   - a reference written "mod.name" where mod is the module name of
//     another defining file (broker.load_profile, with broker.py defining
//     load_profile) belongs to that file.
type resolver struct {
	origin   string          // file of the definition asked about ("" = any)
	defFiles map[string]bool // every file defining the name
	others   map[string]bool // module names of defining files other than origin
	qualRe   *regexp.Regexp
}

func moduleName(rel string) string {
	base := path.Base(rel)
	return strings.TrimSuffix(base, path.Ext(base))
}

func newResolver(name, origin string, defFiles map[string]bool) *resolver {
	r := &resolver{origin: origin, defFiles: defFiles, others: map[string]bool{}}
	if origin == "" || len(defFiles) < 2 {
		return r
	}
	for f := range defFiles {
		if f != origin {
			r.others[moduleName(f)] = true
		}
	}
	delete(r.others, moduleName(origin))
	r.qualRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*\.\s*` + regexp.QuoteMeta(name) + `\b`)
	return r
}

// elsewhere reports whether the reference at rel (line text l) belongs to
// another definition than origin.
func (r *resolver) elsewhere(rel, l string) bool {
	if r.origin == "" || len(r.defFiles) < 2 || rel == r.origin {
		return false
	}
	if r.defFiles[rel] {
		return true
	}
	if r.qualRe != nil {
		for _, m := range r.qualRe.FindAllStringSubmatch(l, -1) {
			if r.others[m[1]] {
				return true
			}
		}
	}
	return false
}

// refSites returns the deduplicated reference sites of name, honoring
// path, tests and the resolver. elsewhere counts references attributed to
// another definition.
func refSites(ix Index, fl *fileLines, name string, opts CallersOptions, res *resolver) (shown []site, hiddenTests, elsewhere int) {
	seen := map[string]bool{}
	for _, rel := range sortedKeys(ix.Tags) {
		tags := ix.Tags[rel]
		if opts.Path != "" && !underPath(rel, opts.Path) {
			continue
		}
		for _, t := range tags {
			if t.Kind != "ref" || t.Name != name || t.Lang == "markdown" {
				continue
			}
			key := fmt.Sprintf("%s:%d", rel, t.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			if res != nil && res.elsewhere(rel, fl.line(rel, t.Line)) {
				elsewhere++
				continue
			}
			if !opts.IncludeTests && IsTestFile(rel) {
				hiddenTests++
				continue
			}
			s := site{file: rel, line: t.Line}
			if fn, q, ok := enclosing(tags, t.Line); ok {
				s.fn, s.scope, s.hasFn = fn, q, true
			}
			shown = append(shown, s)
		}
	}
	sort.Slice(shown, func(i, j int) bool {
		if shown[i].file != shown[j].file {
			return shown[i].file < shown[j].file
		}
		return shown[i].line < shown[j].line
	})
	return shown, hiddenTests, elsewhere
}

// defFilesOf returns the files that define name (markdown excluded).
func defFilesOf(ix Index, name string) map[string]bool {
	out := map[string]bool{}
	for rel, tags := range ix.Tags {
		for _, t := range tags {
			if t.IsDef() && t.Name == name && t.Lang != "markdown" {
				out[rel] = true
				break
			}
		}
	}
	return out
}

type textHit struct {
	file string
	line int
	text string
}

func wordRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_])`)
}

// mentions lists the lines of parsed files that name the symbol as a whole
// word but carry no tag for it: imports, strings, comments, __all__ entries,
// and anything the language query does not capture. A rename must touch
// them too.
func mentions(ix Index, fl *fileLines, name string, opts CallersOptions) (hits []textHit, hiddenTests int) {
	if ix.Read == nil || name == "" {
		return nil, 0
	}
	re := wordRe(name)
	for _, rel := range sortedKeys(ix.Tags) {
		if opts.Path != "" && !underPath(rel, opts.Path) {
			continue
		}
		tags := ix.Tags[rel]
		if len(tags) > 0 && tags[0].Lang == "markdown" {
			continue
		}
		tagged := map[int]bool{}
		for _, t := range tags {
			if t.Name == name {
				tagged[t.Line] = true
			}
		}
		lines := fl.lines(rel)
		if lines == nil || !strings.Contains(strings.Join(lines, "\n"), name) {
			continue
		}
		test := IsTestFile(rel)
		for i, l := range lines {
			if tagged[i+1] || !strings.Contains(l, name) || !re.MatchString(l) {
				continue
			}
			if test && !opts.IncludeTests {
				hiddenTests++
				continue
			}
			hits = append(hits, textHit{file: rel, line: i + 1})
		}
	}
	return hits, hiddenTests
}

// textSites greps every unparsed text file for name as a whole word.
func textSites(ix Index, name string, opts CallersOptions) (hits []textHit, hiddenTests int) {
	if ix.Read == nil || name == "" {
		return nil, 0
	}
	re := wordRe(name)
	for _, rel := range sortedKeys(ix.Files) {
		if _, parsed := ix.Tags[rel]; parsed || ix.Files[rel] > textGrepMaxBytes || ix.Files[rel] == 0 {
			continue
		}
		if parser.FilenameToLang(rel) != "" {
			continue // a parsed language with no tags (e.g. empty or markdown)
		}
		if opts.Path != "" && !underPath(rel, opts.Path) {
			continue
		}
		data, err := ix.Read(rel)
		if err != nil || bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
			continue // unreadable or binary
		}
		if !bytes.Contains(data, []byte(name)) {
			continue
		}
		test := IsTestFile(rel)
		for i, l := range strings.Split(string(data), "\n") {
			if !re.MatchString(l) {
				continue
			}
			if test && !opts.IncludeTests {
				hiddenTests++
				continue
			}
			hits = append(hits, textHit{file: rel, line: i + 1, text: clip(strings.TrimSpace(l))})
		}
	}
	return hits, hiddenTests
}

func underPath(rel, p string) bool {
	p = strings.TrimSuffix(normPath(p), "/")
	return p == "" || rel == p || strings.HasPrefix(rel, p+"/")
}

func normPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	if p == "." || p == "/" {
		return ""
	}
	return strings.TrimSuffix(path.Clean("/" + p)[1:], "/")
}

// Callers lists every reference to a symbol: file:line, the enclosing
// function with its line range, and the line itself, plus whole-word text
// matches in files repomap does not parse. With Depth > 1 it follows the
// enclosing functions outward, by name.
func Callers(ix Index, opts CallersOptions) string {
	if opts.Depth <= 0 {
		opts.Depth = 1
	}
	def := DefaultCallersTokens
	if opts.Depth > 1 {
		def = DefaultCallersDeepTokens
	}
	opts.MaxTokens = clampBudget(opts.MaxTokens, def)
	if opts.Depth > 3 {
		opts.Depth = 3
	}
	if opts.Context < 0 {
		opts.Context = 0
	}
	if opts.Context > 3 {
		opts.Context = 3
	}
	opts.File = normPath(opts.File)
	w := newWriter(opts.MaxTokens)
	fl := newFileLines(ix.Read)

	// Definitions.
	var defs []parser.Tag
	defFiles := map[string]bool{}
	for _, rel := range sortedKeys(ix.Tags) {
		for _, t := range ix.Tags[rel] {
			if t.IsDef() && t.Name == opts.Symbol && t.Lang != "markdown" {
				defFiles[rel] = true
				if opts.File == "" || rel == opts.File {
					defs = append(defs, t)
				}
			}
		}
	}
	origin := opts.File
	if origin == "" && len(defFiles) == 1 {
		for f := range defFiles {
			origin = f
		}
	}
	sites, hiddenTests, elsewhere := refSites(ix, fl, opts.Symbol, opts, newResolver(opts.Symbol, origin, defFiles))
	texts, hiddenTextTests := textSites(ix, opts.Symbol, opts)
	other, hiddenOtherTests := mentions(ix, fl, opts.Symbol, opts)

	files := map[string]bool{}
	for _, s := range sites {
		files[s.file] = true
	}
	for _, t := range texts {
		files[t.file] = true
	}
	switch len(defs) {
	case 0:
		w.add(fmt.Sprintf("%s: no definition found in parsed files\n", opts.Symbol))
	case 1:
		d := defs[0]
		w.add(fmt.Sprintf("%s: defined %s:%s %s\n", opts.Symbol, d.RelFile, span(d), d.Kind))
		if sig := fl.line(d.RelFile, d.Line); sig != "" {
			w.add("  " + sig + "\n")
		}
	default:
		w.add(fmt.Sprintf("%s: %d definitions (references below match by name, not by type):\n", opts.Symbol, len(defs)))
		for i, d := range defs {
			if i == 5 {
				w.add(fmt.Sprintf("  … %d more definitions\n", len(defs)-5))
				break
			}
			w.add(fmt.Sprintf("  %s:%s %s\n", d.RelFile, span(d), d.Kind))
		}
	}
	summary := fmt.Sprintf("%d references in %d files", len(sites)+len(texts), len(files))
	if len(texts) > 0 {
		summary += fmt.Sprintf(" (%d of them text matches in files repomap does not parse)", len(texts))
	}
	if len(other) > 0 {
		summary += fmt.Sprintf("; %d other mentions (imports, strings, comments) listed last", len(other))
	}
	if n := hiddenTests + hiddenTextTests + hiddenOtherTests; n > 0 {
		summary += fmt.Sprintf("; %d more in tests (include_tests=true)", n)
	}
	if elsewhere > 0 {
		summary += fmt.Sprintf("; %d resolved to the other definitions", elsewhere)
	}
	if opts.Path != "" {
		summary += " under " + normPath(opts.Path)
	}
	w.add(summary + "\n")

	omitted := 0
	const reserve = 50 // tokens kept for the truncation note
	for i, s := range sites {
		entry := renderSite(fl, s, opts.Context)
		if !w.fits(entry, reserve) {
			omitted += len(sites) - i
			break
		}
		w.add(entry)
	}
	if omitted == 0 {
		for i, t := range texts {
			entry := fmt.Sprintf("%s:%d (text)\n  %s\n", t.file, t.line, t.text)
			if !w.fits(entry, reserve) {
				omitted += len(texts) - i
				break
			}
			w.add(entry)
		}
	} else {
		omitted += len(texts)
	}

	// Depth 2+: references to the enclosing functions, by name.
	if opts.Depth > 1 && omitted == 0 {
		omitted += callersDeeper(ix, fl, opts, sites, w, reserve)
	}
	if len(other) > 0 {
		if omitted > 0 {
			omitted += len(other)
		} else {
			omitted += writeMentions(w, fl, other, reserve)
		}
	}
	if omitted > 0 {
		w.add(fmt.Sprintf("[truncated at max_tokens=%d: %d more references not shown; narrow with path=, or raise max_tokens]\n", opts.MaxTokens, omitted))
	} else {
		// Small models kept searching after a complete answer (A/B t2, 41-74 calls a run): say so.
		w.add(fmt.Sprintf("[complete: every reference%s is listed above]\n", map[bool]string{true: " to depth " + fmt.Sprint(opts.Depth), false: ""}[opts.Depth > 1]))
	}
	return w.String()
}

// compactSites renders sites as "a.py:3, a.py:9, b.py:4".
func compactSites(ss []site) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = fmt.Sprintf("%s:%d", s.file, s.line)
	}
	return strings.Join(parts, ", ")
}

// mentionsWithText is how many mentions are printed with their line text;
// more are grouped as "a.py: 3, 10". With line numbers only, agents re-read
// every mention to tell an import from a docstring (A/B t4).
const mentionsWithText = 15

// writeMentions prints mentions, with their text when there are few, and
// returns how many did not fit.
func writeMentions(w *writer, fl *fileLines, hits []textHit, reserve int) int {
	if !w.fits("other mentions:\n", reserve) {
		return len(hits)
	}
	w.add("other mentions (imports, strings, comments):\n")
	if len(hits) <= mentionsWithText {
		for i, h := range hits {
			entry := fmt.Sprintf("  %s:%d: %s\n", h.file, h.line, fl.line(h.file, h.line))
			if !w.fits(entry, reserve) {
				return len(hits) - i
			}
			w.add(entry)
		}
		return 0
	}
	for i := 0; i < len(hits); {
		j := i
		var nums []string
		for j < len(hits) && hits[j].file == hits[i].file {
			nums = append(nums, fmt.Sprint(hits[j].line))
			j++
		}
		entry := fmt.Sprintf("  %s: %s\n", hits[i].file, strings.Join(nums, ", "))
		if !w.fits(entry, reserve) {
			return len(hits) - i
		}
		w.add(entry)
		i = j
	}
	return 0
}

func renderSite(fl *fileLines, s site, context int) string {
	var b strings.Builder
	where := "module level"
	if s.hasFn {
		where = fmt.Sprintf("in %s [%s]", s.scope, span(s.fn))
	}
	fmt.Fprintf(&b, "%s:%d %s\n", s.file, s.line, where)
	if context == 0 {
		if l := fl.line(s.file, s.line); l != "" {
			b.WriteString("  " + l + "\n")
		}
		return b.String()
	}
	for n := s.line - context; n <= s.line+context; n++ {
		l := fl.line(s.file, n)
		if n < 1 || (l == "" && n != s.line) {
			continue
		}
		mark := " "
		if n == s.line {
			mark = ">"
		}
		fmt.Fprintf(&b, " %s%d %s\n", mark, n, l)
	}
	return b.String()
}

// callerName is the name a depth-n+1 search uses for a site's scope: the
// function itself, or its class for a constructor.
func callerName(s site) string {
	if !s.hasFn {
		return ""
	}
	n := s.fn.Name
	if n == "__init__" || n == "constructor" || n == "__new__" {
		if i := strings.IndexByte(s.scope, '.'); i > 0 {
			return s.scope[:i]
		}
		return ""
	}
	return n
}

// methodClass returns the class of a site's enclosing method.
func methodClass(s site) (string, bool) {
	if !s.hasFn || s.fn.Kind == "class" {
		return "", false
	}
	i := strings.LastIndexByte(s.scope, '.')
	if i <= 0 {
		return "", false
	}
	return s.scope[:i], true
}

// callersDeeper renders depth 2..opts.Depth and returns how many references
// it had to leave out.
func callersDeeper(ix Index, fl *fileLines, opts CallersOptions, level []site, w *writer, reserve int) int {
	visited := map[string]bool{opts.Symbol: true}
	omitted := 0
	for depth := 2; depth <= opts.Depth; depth++ {
		names := map[string]site{}
		for _, s := range level {
			if n := callerName(s); n != "" && !visited[n] {
				names[n] = s
			}
		}
		if len(names) == 0 {
			break
		}
		w.add(fmt.Sprintf("depth %d: references to the %d enclosing functions above (matched by name)\n", depth, len(names)))
		type group struct {
			name      string
			from      site
			defs      int
			sites     []site
			hidden    int
			resolved  int
			ambiguous []site // by-name matches of a shared method name, listed compactly
		}
		var groups []group
		for _, n := range sortedKeys(names) {
			visited[n] = true
			from := names[n]
			defs := defFilesOf(ix, n)
			sub := opts
			sub.File = ""
			sites, hidden, other := refSites(ix, fl, n, sub, newResolver(n, from.file, defs))
			g := group{name: n, from: from, defs: len(defs), hidden: hidden, resolved: other}
			// A method whose name other classes also define: keep only
			// references in its own file and ones written Class.name; the
			// rest are probably other classes' methods.
			if cls, isMethod := methodClass(from); isMethod && len(defs) > 1 {
				qual := wordRe(cls + "." + n)
				for _, st := range sites {
					if st.file == from.file || qual.MatchString(fl.line(st.file, st.line)) {
						g.sites = append(g.sites, st)
					} else {
						g.ambiguous = append(g.ambiguous, st)
					}
				}
			} else {
				g.sites = sites
			}
			groups = append(groups, g)
		}
		// Unambiguous names first, so a common name cannot crowd them out.
		sort.SliceStable(groups, func(i, j int) bool { return (groups[i].defs > 1) != (groups[j].defs > 1) && groups[j].defs > 1 })
		var next []site
		for _, g := range groups {
			head := fmt.Sprintf("%s (from %s:%d): %d references", g.name, g.from.file, g.from.fn.Line, len(g.sites))
			if g.hidden > 0 {
				head += fmt.Sprintf(", %d in tests", g.hidden)
			}
			if g.defs > 1 {
				head += fmt.Sprintf("; %d files define %s (%d references resolved to them", g.defs, g.name, g.resolved)
				if len(g.ambiguous) > 0 {
					head += fmt.Sprintf(", %d by-name matches elsewhere, probably other classes' %s, listed last", len(g.ambiguous), g.name)
				}
				head += ")"
			}
			if !w.fits(head+"\n", reserve) {
				omitted += len(g.sites)
				continue
			}
			w.add(head + "\n")
			for i, st := range g.sites {
				where := "module level"
				if st.hasFn {
					where = fmt.Sprintf("in %s [%s]", st.scope, span(st.fn))
				}
				entry := fmt.Sprintf("  %s:%d %s: %s\n", st.file, st.line, where, fl.line(st.file, st.line))
				if !w.fits(entry, reserve) {
					omitted += len(g.sites) - i
					break
				}
				w.add(entry)
			}
			if len(g.ambiguous) > 0 {
				line := "  other " + g.name + " (by name, not followed): " + compactSites(g.ambiguous) + "\n"
				if w.fits(line, reserve) {
					w.add(line)
				} else {
					omitted += len(g.ambiguous)
				}
			}
			next = append(next, g.sites...)
		}
		level = next
	}
	return omitted
}

// --- outline -----------------------------------------------------------------

// OutlineOptions configure Outline.
type OutlineOptions struct {
	// Path is a file or a directory ("" = the whole project).
	Path string
	// All includes module-level variables and constants.
	All          bool
	IncludeTests bool
	MaxTokens    int
}

// dirEntryMaxDefs caps the definitions a directory outline lists per file,
// so one huge module cannot take the whole budget.
const dirEntryMaxDefs = 12

var outlineKinds = map[string]bool{"function": true, "method": true, "class": true, "interface": true, "type": true, "def": true}

func keepDef(t parser.Tag, all bool) bool {
	if !t.IsDef() || t.Lang == "markdown" {
		return false
	}
	return all || outlineKinds[t.Kind]
}

// Outline lists the definitions in a file (nested, with line ranges, so a
// ranged read can target one function) or, for a directory, each file's
// line count and top-level definitions, highest-ranked files first.
func Outline(ix Index, opts OutlineOptions) string {
	opts.MaxTokens = clampBudget(opts.MaxTokens, DefaultOutlineTokens)
	p := normPath(opts.Path)
	fl := newFileLines(ix.Read)
	w := newWriter(opts.MaxTokens)
	if tags, ok := ix.Tags[p]; ok && p != "" {
		return outlineFile(fl, w, p, tags, opts)
	}
	if _, ok := ix.Files[p]; ok && p != "" {
		return fmt.Sprintf("%s: %d lines, not parsed (repomap parses .py .go .ts .tsx .js .jsx .mjs .md and python/node shebang scripts); grep or read a range instead\n", p, fl.count(p))
	}
	return outlineDir(fl, w, ix, p, opts)
}

func outlineFile(fl *fileLines, w *writer, rel string, tags []parser.Tag, opts OutlineOptions) string {
	var defs []parser.Tag
	for _, t := range tags {
		if keepDef(t, opts.All) {
			defs = append(defs, t)
		}
	}
	sort.SliceStable(defs, func(i, j int) bool {
		if defs[i].Line != defs[j].Line {
			return defs[i].Line < defs[j].Line
		}
		return defs[i].EndLine > defs[j].EndLine
	})
	w.add(fmt.Sprintf("%s (%d lines, %d definitions)\n", rel, fl.count(rel), len(defs)))
	var stack []parser.Tag
	for i, d := range defs {
		for len(stack) > 0 && stack[len(stack)-1].EndLine < d.Line {
			stack = stack[:len(stack)-1]
		}
		entry := fmt.Sprintf("%s%s %s %s\n", strings.Repeat("  ", len(stack)+1), span(d), d.Kind, d.Name)
		if !w.fits(entry, 40) {
			w.add(fmt.Sprintf("[truncated at max_tokens=%d: %d more definitions from line %d; raise max_tokens]\n", opts.MaxTokens, len(defs)-i, d.Line))
			break
		}
		w.add(entry)
		if d.EndLine > d.Line && scopeKinds[d.Kind] {
			stack = append(stack, d)
		}
	}
	return w.String()
}

func outlineDir(fl *fileLines, w *writer, ix Index, p string, opts OutlineOptions) string {
	var files []string
	hiddenTests, unparsed := 0, 0
	for rel := range ix.Files {
		if !underPath(rel, p) {
			continue
		}
		if _, ok := ix.Tags[rel]; !ok {
			unparsed++
			continue
		}
	}
	for rel := range ix.Tags {
		if !underPath(rel, p) || strings.HasSuffix(rel, ".md") || parser.FilenameToLang(rel) == "markdown" {
			continue
		}
		if !opts.IncludeTests && IsTestFile(rel) {
			hiddenTests++
			continue
		}
		files = append(files, rel)
	}
	if len(files) == 0 {
		return fmt.Sprintf("%s: no parsed source files (%d other files)\n", displayDir(p), unparsed)
	}
	sort.Slice(files, func(i, j int) bool {
		ri, rj := ix.Ranks[files[i]], ix.Ranks[files[j]]
		if ri != rj {
			return ri > rj
		}
		return files[i] < files[j]
	})
	head := fmt.Sprintf("%s: %d source files", displayDir(p), len(files))
	if hiddenTests > 0 {
		head += fmt.Sprintf(", %d test files hidden (include_tests=true)", hiddenTests)
	}
	if unparsed > 0 {
		head += fmt.Sprintf(", %d files not parsed", unparsed)
	}
	head += ". Top-level definitions with start lines; outline a file for nested ranges.\n"
	w.add(head)

	entries := map[string]string{}
	var chosen []string
	const reserve = 60
	used := 0
	for _, rel := range files {
		e := dirEntry(fl, rel, ix.Tags[rel], opts.All)
		t := EstimateTokens(e)
		if !w.fits("", reserve+used+t) {
			continue
		}
		entries[rel] = e
		used += t
		chosen = append(chosen, rel)
	}
	sort.Strings(chosen)
	for _, rel := range chosen {
		w.add(entries[rel])
	}
	if missing := len(files) - len(chosen); missing > 0 {
		var rest []string
		for _, rel := range files {
			if _, ok := entries[rel]; !ok {
				rest = append(rest, rel)
			}
		}
		note := fmt.Sprintf("[truncated at max_tokens=%d: %d lower-ranked files not shown", opts.MaxTokens, missing)
		const suffix = "; outline a subdirectory]\n"
		list := ""
		for _, r := range rest {
			if !w.fits(note+list+", "+r+", …"+suffix, 0) {
				list += ", …"
				break
			}
			if list == "" {
				list = ": " + r
			} else {
				list += ", " + r
			}
		}
		w.add(note + list + suffix)
	}
	return w.String()
}

func displayDir(p string) string {
	if p == "" {
		return "."
	}
	return p + "/"
}

func dirEntry(fl *fileLines, rel string, tags []parser.Tag, all bool) string {
	var top []parser.Tag
	var scopes []parser.Tag
	defs := append([]parser.Tag(nil), tags...)
	sort.SliceStable(defs, func(i, j int) bool { return defs[i].Line < defs[j].Line })
	for _, t := range defs {
		if !keepDef(t, all) {
			continue
		}
		nested := false
		for _, s := range scopes {
			if s.Line < t.Line && s.EndLine >= t.EndLine || (s.Line == t.Line && s.EndLine > t.EndLine) {
				nested = true
				break
			}
		}
		if nested {
			continue
		}
		top = append(top, t)
		if scopeKinds[t.Kind] && t.EndLine > t.Line {
			scopes = append(scopes, t)
		}
	}
	parts := make([]string, 0, len(top))
	extra := 0
	if len(top) > dirEntryMaxDefs {
		extra = len(top) - dirEntryMaxDefs
		top = top[:dirEntryMaxDefs]
	}
	for _, t := range top {
		name := t.Name
		if t.Kind == "class" || t.Kind == "interface" || t.Kind == "type" {
			name = t.Kind + " " + name
		}
		parts = append(parts, fmt.Sprintf("%s %d", name, t.Line))
	}
	if extra > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", extra))
	}
	return fmt.Sprintf("%s (%d lines): %s\n", rel, fl.count(rel), strings.Join(parts, ", "))
}

// --- search ------------------------------------------------------------------

// SearchOptions configure Search.
type SearchOptions struct {
	// Query matches names case-insensitively: exact, then prefix, then
	// substring matches, each group highest-ranked file first.
	Query string
	// Refs also lists references (default: definitions only).
	Refs         bool
	IncludeTests bool
	Path         string
	MaxTokens    int
}

// Search finds definitions (and optionally references) by name.
func Search(ix Index, opts SearchOptions) string {
	opts.MaxTokens = clampBudget(opts.MaxTokens, DefaultSearchTokens)
	q := strings.ToLower(strings.TrimSpace(opts.Query))
	if q == "" {
		return "query is empty\n"
	}
	type hit struct {
		t     parser.Tag
		class int
		rank  float32
	}
	var hits []hit
	hidden := 0
	for rel, tags := range ix.Tags {
		if opts.Path != "" && !underPath(rel, opts.Path) {
			continue
		}
		test := IsTestFile(rel)
		for _, t := range tags {
			if t.Lang == "markdown" || (!opts.Refs && !t.IsDef()) {
				continue
			}
			n := strings.ToLower(t.Name)
			class := 3
			switch {
			case n == q:
				class = 0
			case strings.HasPrefix(n, q):
				class = 1
			case strings.Contains(n, q):
				class = 2
			}
			if class == 3 {
				continue
			}
			if test && !opts.IncludeTests {
				hidden++
				continue
			}
			hits = append(hits, hit{t: t, class: class, rank: ix.Ranks[rel]})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.class != b.class {
			return a.class < b.class
		}
		if a.t.IsDef() != b.t.IsDef() {
			return a.t.IsDef()
		}
		if a.rank != b.rank {
			return a.rank > b.rank
		}
		if a.t.RelFile != b.t.RelFile {
			return a.t.RelFile < b.t.RelFile
		}
		return a.t.Line < b.t.Line
	})
	w := newWriter(opts.MaxTokens)
	what := "definitions"
	if opts.Refs {
		what = "definitions and references"
	}
	head := fmt.Sprintf("%q: %d %s", opts.Query, len(hits), what)
	if hidden > 0 {
		head += fmt.Sprintf(", %d more in tests (include_tests=true)", hidden)
	}
	w.add(head + "\n")
	for i, h := range hits {
		var e string
		if h.t.IsDef() {
			e = fmt.Sprintf("%s:%s %s %s\n", h.t.RelFile, span(h.t), h.t.Kind, h.t.Name)
		} else {
			e = fmt.Sprintf("%s:%d ref %s\n", h.t.RelFile, h.t.Line, h.t.Name)
		}
		if !w.fits(e, 40) {
			w.add(fmt.Sprintf("[truncated at max_tokens=%d: %d more; use a longer query or path=]\n", opts.MaxTokens, len(hits)-i))
			break
		}
		w.add(e)
	}
	return w.String()
}
