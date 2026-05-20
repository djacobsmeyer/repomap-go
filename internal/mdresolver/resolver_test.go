package mdresolver

import "testing"

// project file list shared by the table tests.
var files = []string{
	"README.md",
	"docs/setup.md",
	"docs/auth/README.md",
	"docs/architecture/Architecture Overview.md",
	"guides/Architecture Overview.md",
	"notes/intro.markdown",
}

func TestResolveExternalURLs(t *testing.T) {
	cases := []string{
		"https://example.com",
		"http://example.com/path",
		"https://example.com/page#frag",
		"//cdn.example.com/x",
		"mailto:someone@example.com",
		"ftp://files.example.com/a.md",
	}
	for _, dest := range cases {
		if got := Resolve(dest, "docs/setup.md", files); got != "" {
			t.Errorf("Resolve(%q) = %q, want \"\" (external URL must not create an edge)", dest, got)
		}
	}
}

func TestResolveImageEmbeds(t *testing.T) {
	cases := []string{
		"diagram.png",
		"../assets/photo.jpg",
		"./img/screen.jpeg",
		"anim.gif",
		"icon.webp",
		"logo.svg",
		"spec.pdf",
		"../assets/photo.png#anchor",
	}
	for _, dest := range cases {
		if got := Resolve(dest, "docs/setup.md", files); got != "" {
			t.Errorf("Resolve(%q) = %q, want \"\" (image embed must not create an edge)", dest, got)
		}
	}
}

func TestResolveRelativePaths(t *testing.T) {
	cases := []struct {
		dest   string
		source string
		want   string
	}{
		// From docs/guides/x.md, "../auth/README.md" climbs to docs/ then auth/.
		{"../auth/README.md", "docs/guides/x.md", "docs/auth/README.md"},
		{"./setup.md", "docs/setup.md", "docs/setup.md"},
		{"setup.md", "docs/setup.md", "docs/setup.md"},
		{"docs/setup.md", "README.md", "docs/setup.md"},
		{"./auth/README.md", "docs/setup.md", "docs/auth/README.md"},
		{"../README.md", "docs/setup.md", "README.md"},
		{"auth/README.md", "docs/setup.md", "docs/auth/README.md"},
	}
	for _, c := range cases {
		if got := Resolve(c.dest, c.source, files); got != c.want {
			t.Errorf("Resolve(%q, %q) = %q, want %q", c.dest, c.source, got, c.want)
		}
	}
}

func TestResolveRelativeEscapingRoot(t *testing.T) {
	// A link that escapes the project root cannot resolve to a project file.
	if got := Resolve("../../outside.md", "docs/setup.md", files); got != "" {
		t.Errorf("Resolve escaping root = %q, want \"\"", got)
	}
}

func TestResolveFragmentStripping(t *testing.T) {
	cases := []struct {
		dest   string
		source string
		want   string
	}{
		{"../auth/README.md#setup", "docs/guides/x.md", "docs/auth/README.md"},
		{"./setup.md#install", "docs/setup.md", "docs/setup.md"},
		{"setup.md?v=2#top", "docs/setup.md", "docs/setup.md"},
	}
	for _, c := range cases {
		if got := Resolve(c.dest, c.source, files); got != c.want {
			t.Errorf("Resolve(%q) = %q, want %q (fragment must be stripped)", c.dest, got, c.want)
		}
	}
	// A pure fragment is intra-document — no edge.
	if got := Resolve("#section", "docs/setup.md", files); got != "" {
		t.Errorf("Resolve(\"#section\") = %q, want \"\" (pure fragment must not create an edge)", got)
	}
}

func TestResolveWikilinks(t *testing.T) {
	// Unambiguous basename.
	if got := Resolve("[[intro]]", "README.md", files); got != "notes/intro.markdown" {
		t.Errorf("Resolve([[intro]]) = %q, want notes/intro.markdown", got)
	}
	// Already-unwrapped wikilink form.
	if got := Resolve("intro", "README.md", files); got != "notes/intro.markdown" {
		t.Errorf("Resolve(intro) = %q, want notes/intro.markdown", got)
	}
	// Case-insensitive title match.
	if got := Resolve("[[INTRO]]", "README.md", files); got != "notes/intro.markdown" {
		t.Errorf("Resolve([[INTRO]]) = %q, want notes/intro.markdown", got)
	}
	// Alias form [[Target|Alias]] resolves on Target.
	if got := Resolve("[[intro|See the intro]]", "README.md", files); got != "notes/intro.markdown" {
		t.Errorf("Resolve([[intro|alias]]) = %q, want notes/intro.markdown", got)
	}
	// No match → "".
	if got := Resolve("[[Nonexistent Page]]", "README.md", files); got != "" {
		t.Errorf("Resolve([[Nonexistent Page]]) = %q, want \"\"", got)
	}
}

func TestResolveWikilinkAmbiguityDeterministic(t *testing.T) {
	// "Architecture Overview" exists under two directories. Lexicographically
	// first must win, deterministically, across repeated calls.
	want := "docs/architecture/Architecture Overview.md"
	for i := 0; i < 20; i++ {
		got := Resolve("[[Architecture Overview]]", "README.md", files)
		if got != want {
			t.Fatalf("Resolve([[Architecture Overview]]) = %q, want %q (must be deterministic, lexicographic-first)", got, want)
		}
	}
}

func TestResolveEmptyDest(t *testing.T) {
	if got := Resolve("", "docs/setup.md", files); got != "" {
		t.Errorf("Resolve(\"\") = %q, want \"\"", got)
	}
	if got := Resolve("   ", "docs/setup.md", files); got != "" {
		t.Errorf("Resolve(whitespace) = %q, want \"\"", got)
	}
}
