package cache

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/djacobsmeyer/repomap-go/internal/parser"
)

// TestOpenVersionInvalidate verifies the parser-fingerprint gate: rows cached
// under one version are dropped when the cache is reopened under a different
// version, and a subsequent reopen under the same version serves them again.
func TestOpenVersionInvalidate(t *testing.T) {
	root := t.TempDir()

	c, err := Open(root, "v1")
	if err != nil {
		t.Fatalf("Open(v1): %v", err)
	}
	tags := []parser.Tag{{RelFile: "a.py", Line: 1, Name: "x", Kind: "function", Lang: "python"}}
	c.Set("a.py", 42, tags)
	if got, ok := c.Get("a.py", 42); !ok || len(got) != 1 {
		t.Fatalf("after Set under v1: Get = (%v, %v), want hit", got, ok)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen under a new parser version: the stale row must be gone.
	c2, err := Open(root, "v2")
	if err != nil {
		t.Fatalf("Open(v2): %v", err)
	}
	if got, ok := c2.Get("a.py", 42); ok {
		t.Fatalf("after reopen under v2: Get hit with stale row %v, want miss", got)
	}
	if err := c2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen again under the same version: rows Set under v2 survive.
	c3, err := Open(root, "v2")
	if err != nil {
		t.Fatalf("Open(v2) again: %v", err)
	}
	c3.Set("a.py", 42, tags)
	if err := c3.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	c4, err := Open(root, "v2")
	if err != nil {
		t.Fatalf("Open(v2) second reopen: %v", err)
	}
	defer c4.Close()
	if got, ok := c4.Get("a.py", 42); !ok || len(got) != 1 {
		t.Fatalf("after Set under v2 and same-version reopen: Get = (%v, %v), want hit", got, ok)
	}
}

// TestOpenLegacyDbWithoutMetaPurges is the upgrade case: a tags.db written
// by a pre-versioning build has tag rows but NO meta table / parser_version
// row. Open must treat the missing version like a mismatch and purge the
// stale rows instead of serving them.
func TestOpenLegacyDbWithoutMetaPurges(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".repomap")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "tags.db"))
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE tags (
			file  TEXT PRIMARY KEY,
			mtime INTEGER NOT NULL,
			data  TEXT NOT NULL
		);
	`); err != nil {
		t.Fatalf("create legacy tags table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tags (file, mtime, data) VALUES ('a.py', 42, '[]')`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	c, err := Open(root, "v1")
	if err != nil {
		t.Fatalf("Open over legacy db: %v", err)
	}
	defer c.Close()
	if _, ok := c.Get("a.py", 42); ok {
		t.Fatal("legacy row survived upgrade to the versioned cache; want miss")
	}
}
