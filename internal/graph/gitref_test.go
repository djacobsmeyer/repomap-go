package graph

import (
	"strings"
	"testing"
)

func TestValidGitRefRefusesOptions(t *testing.T) {
	for _, ref := range []string{"--output", "-O/etc/passwd", "-R", "--no-index", "", "a b", "HEAD;rm"} {
		if ValidGitRef(ref) {
			t.Errorf("ValidGitRef(%q) = true", ref)
		}
	}
	for _, ref := range []string{"HEAD", "HEAD~1", "main", "origin/main", "v1.2.3", "abc123^", "feat/x-y"} {
		if !ValidGitRef(ref) {
			t.Errorf("ValidGitRef(%q) = false", ref)
		}
	}
}

func TestGitDiffArgsHardened(t *testing.T) {
	args := GitDiffArgs("main")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--no-ext-diff", "--no-textconv", "core.fsmonitor=false"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %v", want, args)
		}
	}
	eoo := -1
	for i, a := range args {
		if a == "--end-of-options" {
			eoo = i
		}
	}
	if eoo < 0 || eoo+1 >= len(args) || args[eoo+1] != "main" {
		t.Fatalf("ref must follow --end-of-options: %v", args)
	}
	for _, a := range args[eoo+1:] {
		if strings.HasPrefix(a, "-") {
			t.Fatalf("option after --end-of-options: %v", args)
		}
	}
}

func TestGitDiffRejectsOptionRef(t *testing.T) {
	if _, err := GitDiff(t.TempDir(), "--output"); err == nil {
		t.Fatal("GitDiff ran with an option as the ref")
	}
}
