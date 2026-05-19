package graph

import (
	"bufio"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/djacobsmeyer/repomap-go/internal/parser"
)

// ChangedSymbolsResult is the result for the get_changed_symbols MCP tool.
type ChangedSymbolsResult struct {
	ChangedSymbols []ChangedSymbol `json:"changed_symbols"`
	Summary        string          `json:"summary"`
}

// ChangedSymbol describes a def-tag whose definition line falls within a
// changed line range in a unified diff.
type ChangedSymbol struct {
	File        string             `json:"file"`
	Symbol      string             `json:"symbol"`
	Line        int                `json:"line"`
	Kind        string             `json:"kind"`
	BlastRadius *BlastRadiusResult `json:"blast_radius,omitempty"`
}

// LineRange is an inclusive [Start,End] line span on the new (post-image) side.
type LineRange struct{ Start, End int }

var gitRefRE = regexp.MustCompile(`^[a-zA-Z0-9._~^/\-]+$`)

// hunkRE matches a unified-diff hunk header: @@ -a,b +c,d @@
// The +-side count may be omitted (defaults to 1).
var hunkRE = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,([0-9]+))? @@`)

// ParseDiff parses a unified diff string and returns, per relative file path,
// the line ranges (on the new/post-image side) that the diff modifies.
func ParseDiff(diff string) map[string][]LineRange {
	out := map[string][]LineRange{}
	scanner := bufio.NewScanner(strings.NewReader(diff))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var curFile string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// next +++ line will set curFile
			curFile = ""
		case strings.HasPrefix(line, "+++ "):
			rest := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if rest == "/dev/null" {
				curFile = ""
				continue
			}
			// strip leading "b/" prefix from git diff
			if strings.HasPrefix(rest, "b/") {
				rest = rest[2:]
			}
			// strip trailing tab+timestamp if any
			if idx := strings.IndexByte(rest, '\t'); idx >= 0 {
				rest = rest[:idx]
			}
			curFile = rest
		case strings.HasPrefix(line, "@@"):
			if curFile == "" {
				continue
			}
			m := hunkRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			if count == 0 {
				// pure deletion on the new side; map to surrounding line.
				out[curFile] = append(out[curFile], LineRange{Start: start, End: start})
				continue
			}
			out[curFile] = append(out[curFile], LineRange{Start: start, End: start + count - 1})
		}
	}
	return out
}

// GitDiff shells out to `git diff <gitRef> HEAD --unified=0` in projectRoot.
// gitRef is validated against a strict allow-list before execution.
func GitDiff(projectRoot, gitRef string) (string, error) {
	if !gitRefRE.MatchString(gitRef) {
		return "", fmt.Errorf("invalid git ref: %q", gitRef)
	}
	cmd := exec.Command("git", "diff", gitRef, "HEAD", "--unified=0")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return string(out), nil
}

// ChangedSymbols returns def-tags whose line numbers fall within any changed
// LineRange. If includeBlastRadius is true, BlastRadius is attached to each.
func ChangedSymbols(tagsByFile map[string][]parser.Tag, diff map[string][]LineRange, g *FileGraph, includeBlastRadius bool, maxDepth int) ChangedSymbolsResult {
	res := ChangedSymbolsResult{ChangedSymbols: []ChangedSymbol{}}
	if maxDepth <= 0 {
		maxDepth = 3
	}

	files := make([]string, 0, len(diff))
	for f := range diff {
		files = append(files, f)
	}
	sort.Strings(files)

	for _, f := range files {
		ranges := diff[f]
		tags := tagsByFile[f]
		if len(tags) == 0 || len(ranges) == 0 {
			continue
		}
		// Stable order.
		ordered := make([]parser.Tag, len(tags))
		copy(ordered, tags)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].Line < ordered[j].Line })
		for _, t := range ordered {
			if !t.IsDef() {
				continue
			}
			if !inAnyRange(t.Line, ranges) {
				continue
			}
			cs := ChangedSymbol{File: f, Symbol: t.Name, Line: t.Line, Kind: t.Kind}
			if includeBlastRadius {
				br := BlastRadius(g, tagsByFile, t.Name, f, maxDepth)
				cs.BlastRadius = &br
			}
			res.ChangedSymbols = append(res.ChangedSymbols, cs)
		}
	}
	res.Summary = fmt.Sprintf("%d changed symbols across %d files", len(res.ChangedSymbols), len(files))
	return res
}

func inAnyRange(line int, ranges []LineRange) bool {
	for _, r := range ranges {
		if line >= r.Start && line <= r.End {
			return true
		}
	}
	return false
}
