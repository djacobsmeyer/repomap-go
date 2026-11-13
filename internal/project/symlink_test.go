package project

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// symlinkTree builds base/root (the project) and base/outside (a secret the
// project must never read), and returns a started project with no MCP socket.
func symlinkTree(t *testing.T, setup func(root, outside string)) (*Project, string, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "pkg"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(outside, "secret.py"), "def canary_secret_fn():\n    pass\n")
	mustWrite(t, filepath.Join(root, "pkg", "real.py"), "def real_fn():\n    pass\n")
	if setup != nil {
		setup(root, outside)
	}
	p, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.DisableSocket()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return (blocked on a special file?)")
	}
	t.Cleanup(func() { _ = p.Stop() })
	return p, root, outside
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func defsByName(p *Project, q string) map[string][]string {
	out := map[string][]string{}
	for _, tag := range p.SearchIdentifiers(q, "defs", 1000, nil) {
		out[tag.Name] = append(out[tag.Name], tag.RelFile)
	}
	return out
}

func TestIndexSkipsSymlinkToFileOutsideRoot(t *testing.T) {
	p, _, _ := symlinkTree(t, func(root, outside string) {
		if err := os.Symlink(filepath.Join(outside, "secret.py"), filepath.Join(root, "leak.py")); err != nil {
			t.Fatal(err)
		}
	})
	if got := defsByName(p, "canary"); len(got) != 0 {
		t.Fatalf("identifiers from a file outside the root were indexed: %v", got)
	}
	if got := defsByName(p, "real_fn"); len(got["real_fn"]) != 1 {
		t.Fatalf("real file not indexed: %v", got)
	}
}

func TestIndexSymlinkPointingBackInsideIndexesTargetOnce(t *testing.T) {
	p, _, _ := symlinkTree(t, func(root, outside string) {
		if err := os.Symlink("pkg/real.py", filepath.Join(root, "alias.py")); err != nil {
			t.Fatal(err)
		}
	})
	got := defsByName(p, "real_fn")["real_fn"]
	if len(got) != 1 || got[0] != "pkg/real.py" {
		t.Fatalf("want real_fn only in pkg/real.py, got %v", got)
	}
}

func TestIndexSkipsSymlinkedDirOutsideRoot(t *testing.T) {
	p, _, _ := symlinkTree(t, func(root, outside string) {
		if err := os.Symlink(outside, filepath.Join(root, "vendored")); err != nil {
			t.Fatal(err)
		}
	})
	if got := defsByName(p, "canary"); len(got) != 0 {
		t.Fatalf("walked into a directory symlink: %v", got)
	}
}

func TestIndexDoesNotBlockOnFIFO(t *testing.T) {
	// symlinkTree fails the test if Start blocks.
	p, _, _ := symlinkTree(t, func(root, outside string) {
		if err := syscall.Mkfifo(filepath.Join(root, "pipe.py"), 0o644); err != nil {
			t.Skipf("mkfifo unavailable: %v", err)
		}
	})
	if got := defsByName(p, "real_fn"); len(got["real_fn"]) != 1 {
		t.Fatalf("index incomplete: %v", got)
	}
}

func TestHandleChangesRefusesPathsOutsideRoot(t *testing.T) {
	p, _, _ := symlinkTree(t, nil)
	// What a kqueue event naming a symlink's target becomes after
	// filepath.Rel(root, target).
	p.handleChanges([]string{"../outside/secret.py"})
	if got := defsByName(p, "canary"); len(got) != 0 {
		t.Fatalf("handleChanges indexed a path outside the root: %v", got)
	}
}

func TestHandleChangesDropsFileReplacedBySymlink(t *testing.T) {
	p, root, outside := symlinkTree(t, nil)
	f := filepath.Join(root, "pkg", "real.py")
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.py"), f); err != nil {
		t.Fatal(err)
	}
	p.handleChanges([]string{"pkg/real.py"})
	if got := defsByName(p, "canary"); len(got) != 0 {
		t.Fatalf("read through a swapped-in symlink: %v", got)
	}
	if got := defsByName(p, "real_fn"); len(got) != 0 {
		t.Fatalf("stale tags kept for a file that became a symlink: %v", got)
	}
}

func TestRehydrateSeesEditsMadeWhileEvicted(t *testing.T) {
	p, root, _ := symlinkTree(t, nil)
	// Force eviction (as maybeEvict does after 30 idle minutes).
	p.mu.Lock()
	p.lastMCPCall = time.Now().Add(-time.Hour)
	p.mu.Unlock()
	p.maybeEvict()
	p.mu.RLock()
	evicted := p.index == nil
	p.mu.RUnlock()
	if !evicted {
		t.Fatal("project was not evicted")
	}
	// Edit while evicted: the paused watcher drops this event.
	f := filepath.Join(root, "pkg", "real.py")
	mustWrite(t, f, "def renamed_fn():\n    pass\n")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(f, future, future); err != nil {
		t.Fatal(err)
	}
	if got := defsByName(p, "renamed_fn"); len(got["renamed_fn"]) != 1 {
		t.Fatalf("rehydrated index is stale: renamed_fn missing (%v)", got)
	}
	if got := defsByName(p, "real_fn"); len(got) != 0 {
		t.Fatalf("rehydrated index is stale: real_fn still present (%v)", got)
	}
}

func TestCacheDirOption(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "a.py"), "def a_fn():\n    pass\n")
	p, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.DisableSocket()
	p.SetCacheDir(filepath.Join(root, ".pi", "repomap-cache"))
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	if _, err := os.Stat(filepath.Join(root, ".pi", "repomap-cache", "tags.db")); err != nil {
		t.Fatalf("cache not in --cache-dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".repomap")); !os.IsNotExist(err) {
		t.Fatalf("default .repomap created despite cache dir: %v", err)
	}
}

func TestStartRefusesSymlinkedCacheDir(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	elsewhere := filepath.Join(base, "elsewhere")
	for _, d := range []string{root, elsewhere} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, ".repomap")); err != nil {
		t.Fatal(err)
	}
	p, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.DisableSocket()
	if err := p.Start(context.Background()); err == nil {
		p.Stop()
		t.Fatal("Start accepted a symlinked .repomap")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "tags.db")); !os.IsNotExist(err) {
		t.Fatalf("daemon wrote through the symlinked cache dir: %v", err)
	}
}
