// Package mdresolver resolves markdown link destinations and wikilink targets
// to canonical project-relative file paths (RelFile values).
//
// Correct resolution is the load-bearing piece of markdown indexing: a wrong
// target produces a wrong graph edge, which corrupts PageRank, which makes the
// repo map useless. The resolver therefore has explicit, tested handling for
// four cases — external URLs, image embeds, relative paths, and wikilinks —
// and is fully deterministic (ambiguous wikilinks resolve to the
// lexicographically-first match).
package mdresolver

import (
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// imageExts are destination suffixes treated as non-document embeds. A link
// pointing at one of these never produces a graph edge.
var imageExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
	".svg":  true,
	".pdf":  true,
}

// markdownExts are the suffixes considered "a markdown document" for the
// purpose of wikilink basename matching.
var markdownExts = []string{".md", ".markdown"}

// Resolve maps a link destination (from a markdown inline link or a wikilink)
// plus the source file's RelFile to the canonical RelFile of the target
// document. It returns "" for external URLs, image/binary embeds, and targets
// that cannot be resolved.
//
//   - External URLs (http://, https://, mailto:, protocol-relative //…) → ""
//   - Image / binary embeds (.png, .jpg, .pdf, …)                       → ""
//   - Relative paths (./setup.md, ../auth/README.md)                    → joined+normalized RelFile
//   - Wikilinks ([[Architecture Overview]])                             → basename match against allRelFiles
//
// URL fragments (#section) and query strings (?x=1) are stripped before
// resolution. Pure-fragment links (#section) resolve to "" — they are
// intra-document and create no inter-file edge.
func Resolve(dest, sourceRelFile string, allRelFiles []string) string {
	d := strings.TrimSpace(dest)
	if d == "" {
		return ""
	}

	// Wikilink form: [[Target]] or [[Target|Alias]]. The parser may hand us
	// the destination already unwrapped or still wrapped — handle both.
	if strings.HasPrefix(d, "[[") && strings.HasSuffix(d, "]]") {
		d = strings.TrimSuffix(strings.TrimPrefix(d, "[["), "]]")
	}
	isWikilink := !looksLikePath(d) && !isExternal(d)

	// Strip a [[Target|Alias]] alias — the link target is the part before '|'.
	if i := strings.IndexByte(d, '|'); i >= 0 {
		d = strings.TrimSpace(d[:i])
	}

	// Strip fragment and query string. Order matters: fragment first so a
	// "file.md#a?b" oddity still drops both.
	d = stripFragment(d)
	if d == "" {
		// Pure fragment / empty after stripping → intra-document, no edge.
		return ""
	}

	// External URLs never create edges.
	if isExternal(d) {
		return ""
	}

	// Image / binary embeds never create edges.
	if isImageDest(d) {
		return ""
	}

	if isWikilink {
		return resolveWikilink(d, allRelFiles)
	}

	return resolveRelative(d, sourceRelFile)
}

// stripFragment removes a URL fragment and/or query string from dest.
func stripFragment(d string) string {
	if i := strings.IndexByte(d, '#'); i >= 0 {
		d = d[:i]
	}
	if i := strings.IndexByte(d, '?'); i >= 0 {
		d = d[:i]
	}
	return strings.TrimSpace(d)
}

// isExternal reports whether dest is an external URL (has a scheme) or a
// protocol-relative URL.
func isExternal(d string) bool {
	if strings.HasPrefix(d, "//") {
		return true
	}
	lower := strings.ToLower(d)
	for _, scheme := range []string{"http://", "https://", "ftp://", "mailto:", "tel:"} {
		if strings.HasPrefix(lower, scheme) {
			return true
		}
	}
	// Generic scheme detection: "<scheme>:" where scheme is alphanumeric and
	// appears before any slash. Avoids misfiring on Windows-style "C:" by
	// requiring the scheme to be at least two characters.
	if i := strings.IndexByte(d, ':'); i > 1 {
		slash := strings.IndexByte(d, '/')
		if slash == -1 || i < slash {
			scheme := d[:i]
			if isAlphaNum(scheme) {
				return true
			}
		}
	}
	return false
}

func isAlphaNum(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '+' || r == '-' || r == '.') {
			return false
		}
	}
	return s != ""
}

// isImageDest reports whether dest points at an image or other binary embed.
func isImageDest(d string) bool {
	ext := strings.ToLower(path.Ext(d))
	return imageExts[ext]
}

// looksLikePath reports whether dest is shaped like a filesystem path rather
// than a wikilink title. Anything containing a slash, a leading dot, or a
// recognized file extension is treated as a path.
func looksLikePath(d string) bool {
	if strings.ContainsAny(d, "/\\") {
		return true
	}
	if strings.HasPrefix(d, ".") {
		return true
	}
	ext := strings.ToLower(path.Ext(stripFragment(d)))
	return ext != ""
}

// resolveRelative joins a relative path against the source file's directory
// and normalizes it to a clean, forward-slash RelFile.
func resolveRelative(dest, sourceRelFile string) string {
	src := filepath.ToSlash(sourceRelFile)
	dir := path.Dir(src)
	if dir == "." {
		dir = ""
	}
	joined := path.Join(dir, filepath.ToSlash(dest))
	joined = path.Clean(joined)
	// path.Clean can yield ".." prefixes when a link escapes the project
	// root — those are unresolvable within the project.
	if joined == "." || joined == ".." || strings.HasPrefix(joined, "../") {
		return ""
	}
	return joined
}

// resolveWikilink resolves a [[Title]] wikilink by matching its basename
// against the project's file list. An exact basename match wins; if multiple
// files share the basename, the lexicographically-first RelFile is returned
// for determinism.
func resolveWikilink(title string, allRelFiles []string) string {
	want := strings.ToLower(strings.TrimSpace(title))
	if want == "" {
		return ""
	}
	// A wikilink title may itself include an extension or a subpath segment;
	// reduce it to a bare basename for comparison.
	want = strings.ToLower(basenameNoMarkdownExt(want))

	var matches []string
	for _, rf := range allRelFiles {
		base := basenameNoMarkdownExt(filepath.ToSlash(rf))
		if strings.ToLower(base) == want {
			matches = append(matches, filepath.ToSlash(rf))
		}
	}
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[0]
}

// basenameNoMarkdownExt returns the final path component of p with any
// markdown extension stripped.
func basenameNoMarkdownExt(p string) string {
	base := path.Base(p)
	lower := strings.ToLower(base)
	for _, ext := range markdownExts {
		if strings.HasSuffix(lower, ext) {
			return base[:len(base)-len(ext)]
		}
	}
	return base
}
