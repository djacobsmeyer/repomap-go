package parser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/markdown"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// Tag is a single symbol occurrence found in a file.
type Tag struct {
	RelFile string `json:"rel_file"`
	Line    int    `json:"line"`
	Name    string `json:"name"`
	// Kind is one of: "function", "method", "class", "interface",
	// "type", "variable", "constant" for definitions; "ref" for references;
	// or "def" as a fallback for definitions whose specific kind isn't known.
	Kind string `json:"kind"`
	// Lang is the tree-sitter language identifier (e.g. "go", "python",
	// "typescript", "javascript"). Empty if unknown.
	Lang string `json:"lang,omitempty"`
}

// IsDef reports whether the tag represents a definition (any kind other than
// "ref"). Centralizes the def/ref distinction now that Kind has many values.
func (t Tag) IsDef() bool { return t.Kind != "ref" }

// FilenameToLang returns the tree-sitter language identifier for a file path,
// or empty string if unsupported / explicitly skipped.
func FilenameToLang(path string) string {
	base := strings.ToLower(filepath.Base(path))
	// Skip generated Go protobuf files.
	if strings.HasSuffix(base, ".pb.go") {
		return ""
	}
	// Skip env files.
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return ""
	}

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json", ".yaml", ".yml", ".toml", ".lock", ".sum", ".mod",
		".txt", ".css", ".html", ".svg", ".png", ".jpg", ".jpeg", ".gif",
		".ico", ".wasm":
		return ""
	}

	switch ext {
	case ".md", ".markdown":
		return "markdown"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs":
		return "javascript"
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".hpp":
		return "cpp"
	}
	return ""
}

// SCM queries — minimum viable defs + refs per language.
const tsQuery = `
(function_declaration name: (identifier) @name.definition.function)
(method_definition name: (property_identifier) @name.definition.method)
(class_declaration name: (type_identifier) @name.definition.class)
(interface_declaration name: (type_identifier) @name.definition.interface)
(type_alias_declaration name: (type_identifier) @name.definition.type)
(variable_declarator name: (identifier) @name.definition.variable)
(public_field_definition name: (property_identifier) @name.definition.variable)
(call_expression function: (identifier) @name.reference.call)
(call_expression function: (member_expression property: (property_identifier) @name.reference.call))
`

const goQuery = `
(function_declaration name: (identifier) @name.definition.function)
(method_declaration name: (field_identifier) @name.definition.method)
(type_declaration (type_spec name: (type_identifier) @name.definition.type))
(var_declaration (var_spec name: (identifier) @name.definition.variable))
(const_declaration (const_spec name: (identifier) @name.definition.constant))
(call_expression function: (identifier) @name.reference.call)
(call_expression function: (selector_expression field: (field_identifier) @name.reference.call))
`

const pyQuery = `
(function_definition name: (identifier) @name.definition.function)
(class_definition name: (identifier) @name.definition.class)
(assignment left: (identifier) @name.definition.variable)
(call function: (identifier) @name.reference.call)
(call function: (attribute attribute: (identifier) @name.reference.call))
(argument_list (identifier) @name.reference.value)
(keyword_argument value: (identifier) @name.reference.value)
(pair value: (identifier) @name.reference.value)
(assignment right: (identifier) @name.reference.value)
(default_parameter value: (identifier) @name.reference.value)
(typed_default_parameter value: (identifier) @name.reference.value)
(decorator (identifier) @name.reference.value)
(return_statement (identifier) @name.reference.value)
(return_statement (expression_list (identifier) @name.reference.value))
(list (identifier) @name.reference.value)
(tuple (identifier) @name.reference.value)
(set (identifier) @name.reference.value)
(attribute object: (identifier) @name.reference.value)
(subscript value: (identifier) @name.reference.value)
(binary_operator left: (identifier) @name.reference.value)
(binary_operator right: (identifier) @name.reference.value)
(comparison_operator (identifier) @name.reference.value)
(boolean_operator left: (identifier) @name.reference.value)
(boolean_operator right: (identifier) @name.reference.value)
(unary_operator argument: (identifier) @name.reference.value)
(not_operator argument: (identifier) @name.reference.value)
(if_statement condition: (identifier) @name.reference.value)
(while_statement condition: (identifier) @name.reference.value)
(for_statement right: (identifier) @name.reference.value)
(conditional_expression (identifier) @name.reference.value)
(with_item value: (identifier) @name.reference.value)
(with_item value: (as_pattern (identifier) @name.reference.value))
(interpolation expression: (identifier) @name.reference.value)
(lambda body: (identifier) @name.reference.value)
(yield (identifier) @name.reference.value)
(assert_statement (identifier) @name.reference.value)
(pair key: (identifier) @name.reference.value)
(except_clause (identifier) @name.reference.value)
(except_clause (tuple (identifier) @name.reference.value))
(except_clause (as_pattern (identifier) @name.reference.value))
(raise_statement (identifier) @name.reference.value)
(raise_statement cause: (identifier) @name.reference.value)
(type (identifier) @name.reference.value)
(for_in_clause right: (identifier) @name.reference.value)
(if_clause (identifier) @name.reference.value)
(list_comprehension body: (identifier) @name.reference.value)
(set_comprehension body: (identifier) @name.reference.value)
(generator_expression body: (identifier) @name.reference.value)
(list_splat (identifier) @name.reference.value)
(dictionary_splat (identifier) @name.reference.value)
(await (identifier) @name.reference.value)
(augmented_assignment right: (identifier) @name.reference.value)
(elif_clause condition: (identifier) @name.reference.value)
(slice (identifier) @name.reference.value)
(delete_statement (identifier) @name.reference.value)
(parenthesized_expression (identifier) @name.reference.value)
(subscript subscript: (identifier) @name.reference.value)
(attribute attribute: (identifier) @name.reference.value)
`

// schemaVersion is a manual counter for non-query parser changes (Tag
// semantics, capture filtering, the markdown parse path). Bump it whenever
// the parser changes in a way the query strings alone do not capture, so
// tags cached by an older build are invalidated.
const schemaVersion = 1

// CacheVersion returns a hex sha256 fingerprint of the parser's tag schema:
// the schemaVersion counter plus every tree-sitter query and the markdown
// parse-path marker. Any query edit (or schemaVersion bump) changes the
// fingerprint, letting the on-disk tag cache detect a stale schema and drop
// rows parsed by an older build.
func CacheVersion() string {
	fingerprint := strconv.Itoa(schemaVersion) + tsQuery + goQuery + pyQuery + "markdown-v1"
	sum := sha256.Sum256([]byte(fingerprint))
	return hex.EncodeToString(sum[:])
}

// insideFunctionScope reports whether node is nested inside any ancestor
// whose type is one of scopeTypes (e.g. "function_definition", "lambda").
// The node itself is not checked, only its ancestors up to the root.
func insideFunctionScope(node *sitter.Node, scopeTypes ...string) bool {
	if len(scopeTypes) == 0 {
		return false
	}
	want := make(map[string]bool, len(scopeTypes))
	for _, t := range scopeTypes {
		want[t] = true
	}
	for p := node.Parent(); p != nil; p = p.Parent() {
		if want[p.Type()] {
			return true
		}
	}
	return false
}

// kindFromCapture maps tree-sitter capture names like
// "name.definition.function" to our granular Kind values.
// Unknown definition kinds fall back to "def" for backward compatibility.
func kindFromCapture(capName string) string {
	const prefix = "name.definition."
	if !strings.HasPrefix(capName, prefix) {
		return "def"
	}
	sub := capName[len(prefix):]
	switch sub {
	case "function", "method", "class", "interface",
		"type", "variable", "constant":
		return sub
	}
	return "def"
}

func languageAndQuery(lang string) (*sitter.Language, string, bool) {
	switch lang {
	case "typescript", "javascript":
		return typescript.GetLanguage(), tsQuery, true
	case "go":
		return golang.GetLanguage(), goQuery, true
	case "python":
		return python.GetLanguage(), pyQuery, true
	case "markdown":
		// Markdown uses a two-tree parse path (block tree + inline trees),
		// not the standard single-query path. Return !ok so ParseFile routes
		// to parseMarkdownFile instead.
		return nil, "", false
	}
	return nil, "", false
}

// ParseFile parses a single file rooted at `root` (relpath is relative to root)
// and returns the extracted tags. On any soft error (parse failure, query
// failure) it returns an empty slice without surfacing the error.
func ParseFile(root, relpath string) ([]Tag, error) {
	lang := FilenameToLang(relpath)
	if lang == "" {
		return nil, nil
	}

	abs := filepath.Join(root, relpath)
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}

	// Markdown uses a separate two-tree parse path.
	if lang == "markdown" {
		return parseMarkdownFile(relpath, data)
	}

	tsLang, queryStr, ok := languageAndQuery(lang)
	if !ok {
		return nil, nil
	}

	parser := sitter.NewParser()
	parser.SetLanguage(tsLang)

	tree, err := parser.ParseCtx(context.Background(), nil, data)
	if err != nil {
		// soft failure
		return nil, nil
	}
	if tree == nil {
		return nil, nil
	}
	defer tree.Close()

	root_node := tree.RootNode()
	if root_node == nil {
		return nil, nil
	}

	q, err := sitter.NewQuery([]byte(queryStr), tsLang)
	if err != nil {
		// query compile error — soft fail
		return nil, nil
	}
	defer q.Close()

	qc := sitter.NewQueryCursor()
	defer qc.Close()
	qc.Exec(q, root_node)

	var tags []Tag
	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}
		m = qc.FilterPredicates(m, data)
		for _, cap := range m.Captures {
			capName := q.CaptureNameForId(cap.Index)
			kind := ""
			if strings.HasPrefix(capName, "name.definition") {
				kind = kindFromCapture(capName)
			} else if strings.HasPrefix(capName, "name.reference") {
				kind = "ref"
			} else {
				continue
			}
			node := cap.Node
			if node == nil {
				continue
			}
			// Python (Class A fix): a variable definition only counts at module
			// or class-body scope. Assignments inside a function or lambda are
			// locals, not module-level symbols, so drop them to avoid
			// false positives in dead-code analysis.
			if lang == "python" && kind == "variable" &&
				insideFunctionScope(node, "function_definition", "lambda") {
				continue
			}
			// TypeScript/JavaScript (Class A fix, GH-2): a variable_declarator
			// inside a function body is a local. Package-level and class-field
			// declarations (no function ancestor) stay.
			if (lang == "typescript" || lang == "javascript") && kind == "variable" &&
				insideFunctionScope(node, "function_declaration", "function_expression",
					"arrow_function", "method_definition",
					"generator_function_declaration", "generator_function_expression") {
				continue
			}
			// Go (Class A fix, GH-2): var/const specs inside a function or
			// method body are locals. Package-level declarations stay.
			if lang == "go" && (kind == "variable" || kind == "constant") &&
				insideFunctionScope(node, "function_declaration", "method_declaration", "func_literal") {
				continue
			}
			name := node.Content(data)
			if name == "" {
				continue
			}
			// Python: `self` / `cls` can never resolve to a module-level
			// definition; drop their ref captures (they only add edge
			// volume).
			if lang == "python" && kind == "ref" && (name == "self" || name == "cls") {
				continue
			}
			tags = append(tags, Tag{
				RelFile: relpath,
				Line:    int(node.StartPoint().Row) + 1,
				Name:    name,
				Kind:    kind,
				Lang:    lang,
			})
		}
	}
	return tags, nil
}

// wikilinkRe matches Obsidian-style wikilinks: [[Target]] or [[Target|Alias]].
// The captured group is the inner text; the resolver splits off any alias.
var wikilinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// mdHeadingKind maps an ATX marker node type to a granular heading Kind.
func mdHeadingKind(markerType string) string {
	switch markerType {
	case "atx_h1_marker":
		return "heading-1"
	case "atx_h2_marker":
		return "heading-2"
	case "atx_h3_marker":
		return "heading-3"
	case "atx_h4_marker":
		return "heading-4"
	case "atx_h5_marker":
		return "heading-5"
	case "atx_h6_marker":
		return "heading-6"
	}
	return "heading"
}

// parseMarkdownFile parses a markdown document using the dual-tree markdown
// grammar (block tree for headings, inline trees for links). It returns:
//
//   - one def tag per ATX heading (Kind "heading-N", Name = heading text)
//   - one ref tag per inline link (Kind "ref", Name = raw link destination)
//   - one ref tag per wikilink (Kind "ref", Name = raw wikilink target)
//
// Image embeds (`![alt](dest)`) are skipped: in this grammar they parse as a
// distinct `image` node, never `inline_link`, so matching only `inline_link`
// excludes them automatically. Link destinations are stored raw here — the
// graph layer runs them through mdresolver to obtain canonical RelFiles.
func parseMarkdownFile(relpath string, data []byte) ([]Tag, error) {
	tree, err := markdown.ParseCtx(context.Background(), nil, data)
	if err != nil || tree == nil {
		// soft failure
		return nil, nil
	}

	var tags []Tag

	// Walk the block tree. For each atx_heading, emit a heading def. For each
	// block node carrying an inline subtree, walk that subtree for links.
	tree.Iter(func(n *markdown.Node) bool {
		if n.Node == nil {
			return true
		}
		if n.Type() == "atx_heading" {
			if t, ok := headingTag(n.Node, relpath, data); ok {
				tags = append(tags, t)
			}
		}
		if n.Inline != nil {
			collectInlineLinks(n.Inline, relpath, data, &tags)
		}
		return true
	})

	// Wikilinks: the inline grammar parses [[Title]] as a shortcut_link and
	// strips the brackets, so a regex over raw bytes is the reliable path.
	for _, m := range wikilinkRe.FindAllSubmatchIndex(data, -1) {
		// m[2]:m[3] is the captured inner text.
		inner := strings.TrimSpace(string(data[m[2]:m[3]]))
		if inner == "" {
			continue
		}
		line := 1 + strings.Count(string(data[:m[0]]), "\n")
		tags = append(tags, Tag{
			RelFile: relpath,
			Line:    line,
			Name:    inner,
			Kind:    "ref",
			Lang:    "markdown",
		})
	}

	return tags, nil
}

// headingTag builds a heading def tag from an atx_heading node. The heading
// level comes from the ATX marker child; the text comes from the inline child.
func headingTag(heading *sitter.Node, relpath string, data []byte) (Tag, bool) {
	var kind, text string
	for i := 0; i < int(heading.NamedChildCount()); i++ {
		child := heading.NamedChild(i)
		if child == nil {
			continue
		}
		ct := child.Type()
		switch {
		case strings.HasPrefix(ct, "atx_h") && strings.HasSuffix(ct, "_marker"):
			kind = mdHeadingKind(ct)
		case ct == "inline":
			text = strings.TrimSpace(child.Content(data))
		}
	}
	if kind == "" {
		kind = "heading"
	}
	if text == "" {
		return Tag{}, false
	}
	return Tag{
		RelFile: relpath,
		Line:    int(heading.StartPoint().Row) + 1,
		Name:    text,
		Kind:    kind,
		Lang:    "markdown",
	}, true
}

// collectInlineLinks recursively walks an inline subtree and appends a ref tag
// for every inline_link's link_destination. `image` nodes are not recursed
// into, so image-embed destinations are never collected.
func collectInlineLinks(node *sitter.Node, relpath string, data []byte, tags *[]Tag) {
	if node == nil {
		return
	}
	if node.Type() == "image" {
		// Image embed — skip the whole subtree.
		return
	}
	if node.Type() == "inline_link" {
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if child != nil && child.Type() == "link_destination" {
				dest := strings.TrimSpace(child.Content(data))
				if dest != "" {
					*tags = append(*tags, Tag{
						RelFile: relpath,
						Line:    int(node.StartPoint().Row) + 1,
						Name:    dest,
						Kind:    "ref",
						Lang:    "markdown",
					})
				}
			}
		}
		// An inline_link cannot meaningfully nest another inline_link
		// destination; no further recursion needed for this branch.
		return
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		collectInlineLinks(node.NamedChild(i), relpath, data, tags)
	}
}

// SupportedExtensions returns the list of extensions the parser can handle
// (informational helper for walkers).
func SupportedExtensions() []string {
	return []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".go", ".py", ".md", ".markdown"}
}

// Errorf is a small helper to wrap parse errors with context.
func Errorf(file string, err error) error {
	return fmt.Errorf("parse %s: %w", file, err)
}
