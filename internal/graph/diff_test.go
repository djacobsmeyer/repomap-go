package graph

import (
	"testing"

	"github.com/djacobsmeyer/repomap-go/internal/parser"
)

// spanTag builds a Python def tag with an explicit span for attribution
// tests.
func spanTag(name, kind string, line, endLine int) parser.Tag {
	return parser.Tag{RelFile: "src/mod.py", Line: line, EndLine: endLine, Name: name, Kind: kind, Lang: "python"}
}

// TestChangedSymbolsBodyOnlyHunkAttributesEnclosingFunction: a hunk that
// touches only a function's body (no def line in range) is attributed to
// the function with Reason "body" (GH-4).
func TestChangedSymbolsBodyOnlyHunkAttributesEnclosingFunction(t *testing.T) {
	tags := map[string][]parser.Tag{
		"src/mod.py": {spanTag("f", "function", 1, 4)}, // def line 1, body lines 2-4
	}
	diff := map[string][]LineRange{
		"src/mod.py": {{Start: 3, End: 3}}, // body-only hunk
	}
	res := ChangedSymbols(tags, diff, nil, false, 3)
	if len(res.ChangedSymbols) != 1 {
		t.Fatalf("changed symbols = %d, want 1; got %+v", len(res.ChangedSymbols), res.ChangedSymbols)
	}
	cs := res.ChangedSymbols[0]
	if cs.Symbol != "f" || cs.Line != 1 {
		t.Errorf("changed symbol = %s@%d, want f@1; got %+v", cs.Symbol, cs.Line, cs)
	}
	if cs.Reason != "body" {
		t.Errorf("reason = %q, want %q; got %+v", cs.Reason, "body", cs)
	}
	if res.Summary != "1 changed symbols across 1 files (1 attributed to enclosing definitions)" {
		t.Errorf("summary = %q; got %+v", res.Summary, res)
	}
}

// TestChangedSymbolsDefLineHunkReasonDefinition: a hunk covering the def's
// own line yields Reason "definition" (GH-4).
func TestChangedSymbolsDefLineHunkReasonDefinition(t *testing.T) {
	tags := map[string][]parser.Tag{
		"src/mod.py": {spanTag("f", "function", 1, 4)},
	}
	diff := map[string][]LineRange{
		"src/mod.py": {{Start: 1, End: 1}},
	}
	res := ChangedSymbols(tags, diff, nil, false, 3)
	if len(res.ChangedSymbols) != 1 {
		t.Fatalf("changed symbols = %d, want 1; got %+v", len(res.ChangedSymbols), res.ChangedSymbols)
	}
	if res.ChangedSymbols[0].Reason != "definition" {
		t.Errorf("reason = %q, want %q; got %+v", res.ChangedSymbols[0].Reason, "definition", res.ChangedSymbols)
	}
	// No body attributions, so the summary carries no attribution suffix.
	if res.Summary != "1 changed symbols across 1 files" {
		t.Errorf("summary = %q; got %+v", res.Summary, res)
	}
}

// TestChangedSymbolsMethodBodyEditReportsMethodOnly: a hunk in a method body
// inside a class reports only the method — the class's span also intersects
// the range, but the class's own definition line is untouched and the range
// is fully covered by the inner method (GH-4).
func TestChangedSymbolsMethodBodyEditReportsMethodOnly(t *testing.T) {
	tags := map[string][]parser.Tag{
		"src/mod.py": {
			spanTag("C", "class", 1, 5),    // class spans lines 1-5
			spanTag("m", "function", 2, 4), // method spans lines 2-4
		},
	}
	diff := map[string][]LineRange{
		"src/mod.py": {{Start: 3, End: 3}}, // method body only
	}
	res := ChangedSymbols(tags, diff, nil, false, 3)
	if len(res.ChangedSymbols) != 1 {
		t.Fatalf("changed symbols = %d, want 1 (the method only); got %+v", len(res.ChangedSymbols), res.ChangedSymbols)
	}
	cs := res.ChangedSymbols[0]
	if cs.Symbol != "m" {
		t.Errorf("changed symbol = %q, want m; got %+v", cs.Symbol, cs)
	}
	if cs.Reason != "body" {
		t.Errorf("reason = %q, want %q; got %+v", cs.Reason, "body", cs)
	}
}

// TestChangedSymbolsOuterKeptWhenItsDefLineChanged: if the outer's own
// definition line is in a range, both the outer and the inner are reported
// (GH-4).
func TestChangedSymbolsOuterKeptWhenItsDefLineChanged(t *testing.T) {
	tags := map[string][]parser.Tag{
		"src/mod.py": {
			spanTag("C", "class", 1, 5),
			spanTag("m", "function", 2, 4),
		},
	}
	diff := map[string][]LineRange{
		"src/mod.py": {{Start: 1, End: 1}, {Start: 3, End: 3}},
	}
	res := ChangedSymbols(tags, diff, nil, false, 3)
	if len(res.ChangedSymbols) != 2 {
		t.Fatalf("changed symbols = %d, want 2 (class and method); got %+v", len(res.ChangedSymbols), res.ChangedSymbols)
	}
	byName := map[string]ChangedSymbol{}
	for _, cs := range res.ChangedSymbols {
		byName[cs.Symbol] = cs
	}
	if byName["C"].Reason != "definition" {
		t.Errorf("class reason = %q, want definition; got %+v", byName["C"].Reason, byName["C"])
	}
	if byName["m"].Reason != "body" {
		t.Errorf("method reason = %q, want body; got %+v", byName["m"].Reason, byName["m"])
	}
}

// TestChangedSymbolsModuleLevelGapReportsZero: a hunk in module-level code
// between two functions matches no definition span (GH-4).
func TestChangedSymbolsModuleLevelGapReportsZero(t *testing.T) {
	tags := map[string][]parser.Tag{
		"src/mod.py": {
			spanTag("f1", "function", 1, 3),
			spanTag("f2", "function", 10, 12),
		},
	}
	diff := map[string][]LineRange{
		"src/mod.py": {{Start: 7, End: 8}}, // between the two functions
	}
	res := ChangedSymbols(tags, diff, nil, false, 3)
	if len(res.ChangedSymbols) != 0 {
		t.Fatalf("changed symbols = %d, want 0; got %+v", len(res.ChangedSymbols), res.ChangedSymbols)
	}
	if res.Summary != "0 changed symbols across 1 files" {
		t.Errorf("summary = %q; got %+v", res.Summary, res)
	}
}

// TestChangedSymbolsLegacyZeroEndLine: tags cached before spans existed
// (EndLine 0) still match when their Line falls in a range (GH-4).
func TestChangedSymbolsLegacyZeroEndLine(t *testing.T) {
	tags := map[string][]parser.Tag{
		"src/mod.py": {spanTag("f", "function", 1, 0)}, // pre-span cache row
	}
	diff := map[string][]LineRange{
		"src/mod.py": {{Start: 1, End: 1}},
	}
	res := ChangedSymbols(tags, diff, nil, false, 3)
	if len(res.ChangedSymbols) != 1 {
		t.Fatalf("changed symbols = %d, want 1; got %+v", len(res.ChangedSymbols), res.ChangedSymbols)
	}
	if res.ChangedSymbols[0].Reason != "definition" {
		t.Errorf("reason = %q, want definition; got %+v", res.ChangedSymbols[0].Reason, res.ChangedSymbols)
	}
}

// TestChangedSymbolsStableLineOrdering: results are ordered by definition
// line (GH-4).
func TestChangedSymbolsStableLineOrdering(t *testing.T) {
	tags := map[string][]parser.Tag{
		"src/mod.py": {
			spanTag("z", "function", 20, 22),
			spanTag("a", "function", 1, 3),
			spanTag("m", "function", 10, 12),
		},
	}
	diff := map[string][]LineRange{
		"src/mod.py": {{Start: 1, End: 1}, {Start: 10, End: 10}, {Start: 20, End: 20}},
	}
	res := ChangedSymbols(tags, diff, nil, false, 3)
	if len(res.ChangedSymbols) != 3 {
		t.Fatalf("changed symbols = %d, want 3; got %+v", len(res.ChangedSymbols), res.ChangedSymbols)
	}
	for i, want := range []string{"a", "m", "z"} {
		if res.ChangedSymbols[i].Symbol != want {
			t.Errorf("symbol[%d] = %q, want %q; got %+v", i, res.ChangedSymbols[i].Symbol, want, res.ChangedSymbols)
		}
	}
}

// TestParseDiffStillParsesHunks: ParseDiff behavior is unchanged (GH-4
// regression guard): file paths are normalized, hunk ranges are inclusive,
// and pure deletions map to a single line.
func TestParseDiffStillParsesHunks(t *testing.T) {
	diff := "diff --git a/src/mod.py b/src/mod.py\n" +
		"index 111..222 100644\n" +
		"--- a/src/mod.py\n" +
		"+++ b/src/mod.py\n" +
		"@@ -1,3 +1,2 @@\n" +
		" keep\n" +
		"-gone\n" +
		" keep2\n" +
		"@@ -10,2 +9 @@\n" +
		"-a\n" +
		"-b\n"
	got := ParseDiff(diff)
	ranges, ok := got["src/mod.py"]
	if !ok {
		t.Fatalf("missing src/mod.py; got %v", got)
	}
	if len(ranges) != 2 {
		t.Fatalf("ranges = %v, want 2", ranges)
	}
	if ranges[0].Start != 1 || ranges[0].End != 2 {
		t.Errorf("range[0] = %+v, want {1 2}", ranges[0])
	}
	// count 0 on the new side: pure deletion maps to the single start line.
	if ranges[1].Start != 9 || ranges[1].End != 9 {
		t.Errorf("range[1] = %+v, want {9 9}", ranges[1])
	}
}
