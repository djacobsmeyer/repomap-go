package parser

import (
	"os"
	"path/filepath"
	"testing"
)

// writeMD writes a markdown file under root and returns its relpath.
func writeMD(t *testing.T, root, rel, body string) string {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestFilenameToLangMarkdown(t *testing.T) {
	for _, p := range []string{"README.md", "docs/Guide.MARKDOWN", "x.markdown"} {
		if got := FilenameToLang(p); got != "markdown" {
			t.Errorf("FilenameToLang(%q) = %q, want markdown", p, got)
		}
	}
	// Images must still resolve to "" (unsupported).
	for _, p := range []string{"a.png", "b.JPG", "c.gif"} {
		if got := FilenameToLang(p); got != "" {
			t.Errorf("FilenameToLang(%q) = %q, want \"\"", p, got)
		}
	}
}

func TestParseMarkdownHeadings(t *testing.T) {
	root := t.TempDir()
	rel := writeMD(t, root, "doc.md", "# Top Title\n\n## Second Level\n\n### Third Level\n\nbody text\n")
	tags, err := ParseFile(root, rel)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"Top Title":    "heading-1",
		"Second Level": "heading-2",
		"Third Level":  "heading-3",
	}
	got := map[string]string{}
	for _, tg := range tags {
		if tg.Kind == "ref" {
			continue
		}
		if tg.Lang != "markdown" {
			t.Errorf("heading tag %q has Lang %q, want markdown", tg.Name, tg.Lang)
		}
		if tg.Line < 1 {
			t.Errorf("heading tag %q has non-positive Line %d", tg.Name, tg.Line)
		}
		got[tg.Name] = tg.Kind
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("heading %q: got kind %q, want %q", name, got[name], kind)
		}
	}
}

func TestParseMarkdownLinksAndImages(t *testing.T) {
	root := t.TempDir()
	body := "# Doc\n\n" +
		"See [setup](./setup.md) and [ext](https://example.com).\n\n" +
		"![diagram](diagram.png)\n\n" +
		"A [[Wiki Target]] reference.\n"
	rel := writeMD(t, root, "doc.md", body)
	tags, err := ParseFile(root, rel)
	if err != nil {
		t.Fatal(err)
	}

	var refs []string
	for _, tg := range tags {
		if tg.Kind == "ref" {
			refs = append(refs, tg.Name)
		}
	}

	hasRef := func(name string) bool {
		for _, r := range refs {
			if r == name {
				return true
			}
		}
		return false
	}

	if !hasRef("./setup.md") {
		t.Errorf("expected ref for ./setup.md, got refs=%v", refs)
	}
	if !hasRef("https://example.com") {
		t.Errorf("expected raw ref for external URL (resolver filters it later), got refs=%v", refs)
	}
	if !hasRef("Wiki Target") {
		t.Errorf("expected wikilink ref 'Wiki Target', got refs=%v", refs)
	}
	// The image destination must NOT appear as a ref.
	if hasRef("diagram.png") {
		t.Errorf("image embed destination must not be emitted as a ref, got refs=%v", refs)
	}
}

func TestParseMarkdownEmptyFile(t *testing.T) {
	root := t.TempDir()
	rel := writeMD(t, root, "empty.md", "")
	tags, err := ParseFile(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 0 {
		t.Errorf("empty markdown file produced %d tags, want 0", len(tags))
	}
}
