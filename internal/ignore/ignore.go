// Package ignore provides a unified ignore-matcher for project indexing.
//
// It combines:
//
//   - A hardcoded default blocklist of directory names (build outputs,
//     framework caches, VCS metadata) and file glob patterns (generated /
//     declaration files).
//   - An optional .repomapignore at the project root.
//   - Every .gitignore found while walking the project tree. Each file's
//     patterns apply to its own directory subtree.
//
// The implementation handles the 90% of gitignore syntax — `#` comments,
// blank lines, `!negation`, `dir/` directory-only patterns, `**/pattern`
// double-star, and plain filenames/path suffixes. It is not a strict
// gitignore conformance implementation.
package ignore

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// defaultIgnoreDirs is the set of directory basenames always excluded from
// indexing and watching.
var defaultIgnoreDirs = map[string]bool{
	".git":          true,
	"node_modules":  true,
	".repomap":      true,
	"vendor":        true,
	"dist":          true,
	"build":         true,
	"out":           true,
	".angular":      true,
	".next":         true,
	".nuxt":         true,
	".svelte-kit":   true,
	"__pycache__":   true,
	".pytest_cache": true,
	".mypy_cache":   true,
	"coverage":      true,
	".nyc_output":   true,
	".turbo":        true,
	".cache":        true,
}

// defaultIgnoreFilePatterns are filename globs that should always be skipped.
var defaultIgnoreFilePatterns = []string{
	"*.d.ts",   // TypeScript declaration files — generated
	"*.min.js", // minified JS
	"*.pb.go",  // protobuf-generated Go
	"*.gen.go", // generated Go
}

// pattern is a parsed gitignore-style rule.
type pattern struct {
	// scope is the directory (relative to project root, forward slashes,
	// no trailing slash) within which this pattern applies. Empty string
	// means project root.
	scope string
	// raw is the original pattern text (after stripping `!` and trailing `/`).
	raw string
	// negate is true if the rule starts with `!`.
	negate bool
	// dirOnly is true if the rule ends with `/`.
	dirOnly bool
	// hasSlash is true if the pattern contains a `/` (other than a leading
	// or trailing one) — then it matches paths relative to its scope.
	hasSlash bool
	// doubleStar is true if the pattern contains `**` — recursive match.
	doubleStar bool
}

// Matcher holds compiled ignore rules for a project root.
type Matcher struct {
	root     string
	patterns []pattern
}

// New constructs a Matcher rooted at `root`. It loads .repomapignore (if
// present) and walks the tree picking up every .gitignore. Returns a
// usable Matcher even if no ignore files exist (defaults still apply).
func New(root string) (*Matcher, error) {
	m := &Matcher{root: root}

	// .repomapignore at root, if present.
	rmPath := filepath.Join(root, ".repomapignore")
	if data, err := os.ReadFile(rmPath); err == nil {
		m.appendPatterns("", data)
	}

	// Walk and pick up every .gitignore. We don't recurse into directories
	// that are already known to be ignored by the defaults — those trees
	// can contain millions of files (node_modules etc).
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if rel != "." {
				base := filepath.Base(path)
				if defaultIgnoreDirs[base] {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if info.Name() != ".gitignore" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		dir := filepath.Dir(path)
		scope, _ := filepath.Rel(root, dir)
		scope = filepath.ToSlash(scope)
		if scope == "." {
			scope = ""
		}
		m.appendPatterns(scope, data)
		return nil
	})

	return m, nil
}

func (m *Matcher) appendPatterns(scope string, data []byte) {
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negate := false
		if strings.HasPrefix(line, "!") {
			negate = true
			line = line[1:]
		}
		dirOnly := false
		if strings.HasSuffix(line, "/") {
			dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if line == "" {
			continue
		}
		// Normalize a leading "/" — anchor to scope root.
		line = strings.TrimPrefix(line, "/")
		hasSlash := strings.Contains(line, "/")
		doubleStar := strings.Contains(line, "**")
		m.patterns = append(m.patterns, pattern{
			scope:      scope,
			raw:        line,
			negate:     negate,
			dirOnly:    dirOnly,
			hasSlash:   hasSlash,
			doubleStar: doubleStar,
		})
	}
}

// ShouldIgnore reports whether the given absolute path should be excluded
// from indexing and watching. isDir indicates whether path is a directory.
func (m *Matcher) ShouldIgnore(absPath string, isDir bool) bool {
	if m == nil {
		return false
	}
	rel, err := filepath.Rel(m.root, absPath)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" {
		return false
	}

	// 1. Hardcoded directory blocklist — applies to any path whose
	//    components include a known bad directory name.
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if defaultIgnoreDirs[p] {
			return true
		}
	}

	if !isDir {
		base := filepath.Base(rel)
		// 2. Hardcoded file pattern blocklist.
		for _, pat := range defaultIgnoreFilePatterns {
			if matched, _ := filepath.Match(pat, base); matched {
				return true
			}
		}
		// SQLite shard files we manage ourselves.
		if strings.HasSuffix(base, ".db") || strings.HasSuffix(base, ".db-wal") || strings.HasSuffix(base, ".db-shm") {
			return true
		}
	}

	// 3. gitignore / repomapignore patterns. Last-match-wins (gitignore
	//    semantics), so iterate in order.
	ignored := false
	for _, p := range m.patterns {
		if !p.matches(rel, isDir) {
			continue
		}
		if p.negate {
			ignored = false
		} else {
			ignored = true
		}
	}
	return ignored
}

// matches reports whether the rule matches a path (forward-slash, relative
// to project root). isDir is the type of the path being tested.
func (p pattern) matches(rel string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	// Only patterns whose scope is an ancestor of (or equal to) rel apply.
	if p.scope != "" {
		if rel != p.scope && !strings.HasPrefix(rel, p.scope+"/") {
			return false
		}
	}
	// Compute the path relative to the scope.
	sub := rel
	if p.scope != "" {
		sub = strings.TrimPrefix(rel, p.scope+"/")
	}

	pat := p.raw

	if p.doubleStar {
		return matchDoubleStar(pat, sub)
	}

	if p.hasSlash {
		// Anchored: match the full sub path.
		if ok, _ := filepath.Match(pat, sub); ok {
			return true
		}
		// Also try matching as a prefix-dir (so `foo/bar` matches `foo/bar/baz`).
		if strings.HasPrefix(sub, pat+"/") {
			return true
		}
		return false
	}

	// Bare pattern — match against any path component or the basename.
	if ok, _ := filepath.Match(pat, filepath.Base(sub)); ok {
		return true
	}
	// Match if any path component equals pat.
	for _, comp := range strings.Split(sub, "/") {
		if ok, _ := filepath.Match(pat, comp); ok {
			return true
		}
	}
	return false
}

// matchDoubleStar handles patterns containing `**` segments. It's a simple
// recursive matcher — splits on `**` and checks that each fragment appears
// in order in the candidate path.
func matchDoubleStar(pat, sub string) bool {
	// Normalize.
	pat = strings.ReplaceAll(pat, "/**/", "/**/")
	// Trivial: "**/foo" or "**/foo/bar"
	if strings.HasPrefix(pat, "**/") {
		rest := strings.TrimPrefix(pat, "**/")
		// Try every suffix of sub.
		parts := strings.Split(sub, "/")
		for i := 0; i <= len(parts); i++ {
			candidate := strings.Join(parts[i:], "/")
			if !strings.Contains(rest, "**") {
				if ok, _ := filepath.Match(rest, candidate); ok {
					return true
				}
				if strings.HasPrefix(candidate, rest+"/") {
					return true
				}
			} else if matchDoubleStar(rest, candidate) {
				return true
			}
		}
		return false
	}
	// General case: split on /**/ and require fragments appear in order.
	fragments := strings.Split(pat, "/**/")
	if len(fragments) == 1 {
		ok, _ := filepath.Match(pat, sub)
		return ok
	}
	// First fragment must prefix-match.
	first := fragments[0]
	if !strings.HasPrefix(sub, first+"/") && sub != first {
		// Allow glob on first fragment too.
		if ok, _ := filepath.Match(first, sub); !ok {
			return false
		}
	}
	remainder := strings.TrimPrefix(sub, first+"/")
	rest := strings.Join(fragments[1:], "/**/")
	return matchDoubleStar(rest, remainder)
}
