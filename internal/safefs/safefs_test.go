package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// layout makes root/ and outside/ side by side, with a secret outside root.
func layout(t *testing.T) (root, outside, secret string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{root, outside, filepath.Join(root, "pkg")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret = filepath.Join(outside, "secret.py")
	write(t, secret, "def canary_secret_fn():\n    pass\n")
	write(t, filepath.Join(root, "pkg", "real.py"), "def real_fn():\n    pass\n")
	return root, outside, secret
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadRegularPlainFile(t *testing.T) {
	root, _, _ := layout(t)
	data, err := ReadRegular(root, "pkg/real.py", 0)
	if err != nil || string(data) != "def real_fn():\n    pass\n" {
		t.Fatalf("got %q, %v", data, err)
	}
}

func TestReadRegularRefusesSymlinkToOutsideFile(t *testing.T) {
	root, _, secret := layout(t)
	if err := os.Symlink(secret, filepath.Join(root, "leak.py")); err != nil {
		t.Fatal(err)
	}
	data, err := ReadRegular(root, "leak.py", 0)
	if !errors.Is(err, ErrSymlink) || data != nil {
		t.Fatalf("want ErrSymlink and no data, got %q, %v", data, err)
	}
}

func TestReadRegularRefusesRelativeSymlinkToOutsideFile(t *testing.T) {
	root, _, _ := layout(t)
	if err := os.Symlink("../outside/secret.py", filepath.Join(root, "pkg", "..", "rel.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegular(root, "rel.py", 0); !errors.Is(err, ErrSymlink) {
		t.Fatalf("want ErrSymlink, got %v", err)
	}
}

func TestReadRegularRefusesSymlinkPointingBackInside(t *testing.T) {
	// The link is refused, but the target is still readable under its own
	// name, so nothing inside the root is lost.
	root, _, _ := layout(t)
	if err := os.Symlink("pkg/real.py", filepath.Join(root, "alias.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegular(root, "alias.py", 0); !errors.Is(err, ErrSymlink) {
		t.Fatalf("want ErrSymlink for in-root link, got %v", err)
	}
	if _, err := ReadRegular(root, "pkg/real.py", 0); err != nil {
		t.Fatalf("target must stay readable: %v", err)
	}
}

func TestReadRegularRefusesSymlinkedParentDirOutside(t *testing.T) {
	root, outside, _ := layout(t)
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	data, err := ReadRegular(root, "linkdir/secret.py", 0)
	if err == nil || data != nil {
		t.Fatalf("read through a directory symlink leaving the root: %q", data)
	}
}

func TestReadRegularRefusesDotDot(t *testing.T) {
	root, _, _ := layout(t)
	for _, rel := range []string{"../outside/secret.py", "pkg/../../outside/secret.py", "/etc/hosts", "", ".", ".."} {
		if _, err := ReadRegular(root, rel, 0); !errors.Is(err, ErrOutside) {
			t.Errorf("%q: want ErrOutside, got %v", rel, err)
		}
	}
}

func TestReadRegularFIFODoesNotBlock(t *testing.T) {
	root, _, _ := layout(t)
	fifo := filepath.Join(root, "pipe.py")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := ReadRegular(root, "pipe.py", 0); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("want ErrNotRegular, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ReadRegular blocked on a FIFO")
	}
}

func TestReadRegularSizeCap(t *testing.T) {
	root, _, _ := layout(t)
	write(t, filepath.Join(root, "big.py"), string(make([]byte, 101)))
	if _, err := ReadRegular(root, "big.py", 100); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	if _, err := ReadRegular(root, "big.py", 101); err != nil {
		t.Fatalf("at the cap must pass: %v", err)
	}
}

func TestRelInside(t *testing.T) {
	cases := map[string]bool{
		"a.py": true, "a/b.py": true, "./a.py": true, "a/../b.py": true,
		"": false, ".": false, "..": false, "../a.py": false, "a/../../b": false, "/abs": false,
	}
	for rel, want := range cases {
		if got := RelInside(rel); got != want {
			t.Errorf("RelInside(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestDirInside(t *testing.T) {
	root, outside, _ := layout(t)
	if err := DirInside(root, filepath.Join(root, ".repomap")); err != nil {
		t.Fatalf("missing in-root dir must be fine: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".repomap")); err != nil {
		t.Fatal(err)
	}
	if err := DirInside(root, filepath.Join(root, ".repomap")); err == nil {
		t.Fatal("symlinked cache dir accepted")
	}
	if err := os.Symlink(outside, filepath.Join(root, "pkg", "sub")); err != nil {
		t.Fatal(err)
	}
	if err := DirInside(root, filepath.Join(root, "pkg", "sub", "cache")); err == nil {
		t.Fatal("cache dir below a symlinked component accepted")
	}
	if err := DirInside(root, outside); err == nil {
		t.Fatal("dir outside root accepted")
	}
}
