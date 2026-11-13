package watcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRelInside(t *testing.T) {
	w := &Watcher{root: "/r"}
	if rel, ok := w.relInside("/r/a/b.py"); !ok || rel != "a/b.py" {
		t.Fatalf("got %q %v", rel, ok)
	}
	for _, name := range []string{"/outside/secret.py", "/r/../x.py", "/r"} {
		if _, ok := w.relInside(name); ok {
			t.Errorf("relInside(%q) accepted", name)
		}
	}
}

// A symlink inside the root to a file outside it: writes to the target must
// never surface as an event path outside the root.
func TestNoEventsOutsideRootThroughSymlink(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(outside, "secret.py")
	if err := os.WriteFile(secret, []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "leak.py")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "leakdir")); err != nil {
		t.Fatal(err)
	}
	w, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(secret, []byte("x = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "new.py"), []byte("y = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A legitimate change, so we know the watcher is alive.
	if err := os.WriteFile(filepath.Join(root, "ok.py"), []byte("z = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	sawOK := false
	for !sawOK {
		select {
		case batch := <-w.Events():
			for _, rel := range batch {
				if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
					t.Fatalf("event path leaves the root: %q", rel)
				}
				if rel == "ok.py" {
					sawOK = true
				}
			}
		case <-deadline:
			t.Fatal("no event for ok.py")
		}
	}
}
