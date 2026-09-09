package daemon

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// socketInode returns the inode of the file at path.
func socketInode(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat %s: unexpected Sys() type %T", path, fi.Sys())
	}
	return st.Ino
}

// listenUnixNoUnlink listens on path with unlink-on-close disabled, so
// closing the listener cannot delete a file that now belongs to someone else.
func listenUnixNoUnlink(t *testing.T, path string) net.Listener {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	ul, ok := l.(*net.UnixListener)
	if !ok {
		t.Fatalf("listener for %s is not a *net.UnixListener", path)
	}
	ul.SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// shortTempDir returns a temp directory short enough for a Unix socket path
// (macOS sun_path is 104 bytes; the default TMPDIR under /var/folders is too
// deep for "dir/ctl.sock").
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "repomap-dtest-")
	if err != nil {
		t.Fatalf("mkdir temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestRemoveSocketIfOwnedLeavesStolenPath covers the production incident: a
// second daemon steals our socket path, then we exit. Our shutdown must NOT
// remove the file that now belongs to the other daemon.
func TestRemoveSocketIfOwnedLeavesStolenPath(t *testing.T) {
	p := filepath.Join(shortTempDir(t), "ctl.sock")

	l1 := listenUnixNoUnlink(t, p)
	ino1 := socketInode(t, p)

	// Simulate the path being stolen: our file is replaced by a new socket
	// file with a different inode.
	if err := os.Remove(p); err != nil {
		t.Fatalf("remove stolen path: %v", err)
	}
	listenUnixNoUnlink(t, p)
	ino2 := socketInode(t, p)
	if ino1 == ino2 {
		t.Fatalf("test setup broken: expected distinct inodes")
	}
	// The old daemon exits.
	_ = l1.Close()

	removeSocketIfOwned(p, ino1)

	if _, err := os.Stat(p); err != nil {
		t.Fatalf("socket at %s was removed although we no longer owned it: %v", p, err)
	}
	// The new daemon's listener must still accept connections.
	c, err := net.Dial("unix", p)
	if err != nil {
		t.Fatalf("dial socket now owned by L2: %v", err)
	}
	_ = c.Close()
}

// TestRemoveSocketIfOwnedRemovesOwnPath: while we still own the path, the
// helper removes it.
func TestRemoveSocketIfOwnedRemovesOwnPath(t *testing.T) {
	p := filepath.Join(shortTempDir(t), "ctl.sock")

	l1 := listenUnixNoUnlink(t, p)
	ino1 := socketInode(t, p)
	_ = l1.Close()

	removeSocketIfOwned(p, ino1)

	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, stat err = %v", p, err)
	}
}

// TestRemoveSocketIfOwnedMissingPath: a missing path is a no-op, not an error.
func TestRemoveSocketIfOwnedMissingPath(t *testing.T) {
	removeSocketIfOwned(filepath.Join(t.TempDir(), "nope.sock"), 123)
}
