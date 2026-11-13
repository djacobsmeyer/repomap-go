package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSymlinkedIgnoreFilesAreNotRead(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The "secret" outside the root is a valid ignore file; if it were read,
	// its pattern would hide secret_named.py.
	secret := filepath.Join(base, "secret")
	if err := os.WriteFile(secret, []byte("secret_named.py\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{".repomapignore", filepath.Join("sub", ".gitignore")} {
		if err := os.Symlink(secret, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(root, "secret_named.py"), filepath.Join(root, "sub", "secret_named.py")} {
		if m.ShouldIgnore(f, false) {
			t.Errorf("%s ignored: a symlinked ignore file was read", f)
		}
	}
}

func TestRegularIgnoreFilesStillWork(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("gen_*.py\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if !m.ShouldIgnore(filepath.Join(root, "gen_x.py"), false) {
		t.Fatal(".gitignore pattern not applied")
	}
}
