package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenDirRefusesSymlinkedDefaultDir(t *testing.T) {
	base := t.TempDir()
	root, elsewhere := filepath.Join(base, "root"), filepath.Join(base, "elsewhere")
	for _, d := range []string{root, elsewhere} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, ".repomap")); err != nil {
		t.Fatal(err)
	}
	if c, err := Open(root, "v1"); err == nil {
		c.Close()
		t.Fatal("Open accepted a symlinked .repomap")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "tags.db")); !os.IsNotExist(err) {
		t.Fatalf("tags.db written outside the project: %v", err)
	}
}

func TestOpenDirRefusesSymlinkedDBFile(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, ".repomap"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "victim")
	if err := os.Symlink(target, filepath.Join(root, ".repomap", "tags.db")); err != nil {
		t.Fatal(err)
	}
	if c, err := Open(root, "v1"); err == nil {
		c.Close()
		t.Fatal("Open accepted a symlinked tags.db")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("SQLite created the symlink target: %v", err)
	}
}

func TestOpenDirExplicitDirOutsideRoot(t *testing.T) {
	base := t.TempDir()
	root, cdir := filepath.Join(base, "root"), filepath.Join(base, "cache")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := OpenDir(root, cdir, "v1")
	if err != nil {
		t.Fatalf("operator-chosen cache dir refused: %v", err)
	}
	c.Close()
	if _, err := os.Stat(filepath.Join(cdir, "tags.db")); err != nil {
		t.Fatal(err)
	}
}
