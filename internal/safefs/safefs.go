// Package safefs reads files that live inside a project root without ever
// following a symlink and without ever leaving the root.
//
// The daemon runs unconfined while the trees it indexes may be written by
// confined agents (or contain hostile checkouts). A symlink "x.py ->
// ~/secret.py" inside a project must not make the daemon read the target, a
// FIFO named "x.py" must not wedge it, and a 10 GB "x.py" must not be pulled
// into its heap. Every content read of a project file goes through ReadRegular.
package safefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// MaxFileBytes is the largest file ReadRegular returns. Larger files are
// skipped: they are almost always generated, and tree-sitter on them is slow.
const MaxFileBytes = 8 << 20

// Errors returned by ReadRegular. Callers treat all of them as "skip this file".
var (
	ErrSymlink    = errors.New("safefs: refusing to read through a symlink")
	ErrNotRegular = errors.New("safefs: not a regular file")
	ErrTooLarge   = errors.New("safefs: file too large")
	ErrOutside    = errors.New("safefs: path leaves the project root")
	ErrChanged    = errors.New("safefs: file changed while opening")
)

// RelInside reports whether rel (slash or OS separated, relative to a root)
// names something inside that root: not absolute, no ".." component, not
// empty. It is a lexical check; ReadRegular also enforces it on the
// filesystem.
func RelInside(rel string) bool {
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	return true
}

// ReadRegular returns the contents of root/rel if and only if rel is inside
// root, the final component is a regular file (not a symlink, FIFO, socket or
// device) and it is at most max bytes (max <= 0 means MaxFileBytes).
//
// Escapes through symlinked parent directories or ".." are refused by
// os.Root, which resolves each component with openat and O_NOFOLLOW, so the
// check cannot be raced. A final-component symlink is refused even when its
// target is inside the root: the target is indexed under its own name. The
// file is opened O_NONBLOCK so a FIFO cannot block the open, and the opened
// descriptor is compared with the Lstat result so a swap between the two is
// refused.
func ReadRegular(root, rel string, max int64) ([]byte, error) {
	if max <= 0 {
		max = MaxFileBytes
	}
	if !RelInside(rel) {
		return nil, ErrOutside
	}
	rel = filepath.FromSlash(rel)
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	li, err := r.Lstat(rel)
	if err != nil {
		return nil, mapRootErr(err)
	}
	if li.Mode()&fs.ModeSymlink != 0 {
		return nil, ErrSymlink
	}
	if !li.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if li.Size() > max {
		return nil, ErrTooLarge
	}

	f, err := r.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, mapRootErr(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if !os.SameFile(li, fi) {
		return nil, ErrChanged
	}
	// Read at most max+1 bytes: a file that grew past the cap after the
	// Lstat is still refused instead of being read without bound.
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, ErrTooLarge
	}
	return data, nil
}

// IsSymlink reports whether path itself (not its target) is a symlink.
func IsSymlink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// DirInside checks that dir exists or can be created as a real directory
// strictly inside (or equal to) root, with no symlink anywhere between root
// and dir. It is used for the on-disk cache, which the daemon writes.
func DirInside(root, dir string) error {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || !(rel == "." || RelInside(rel)) {
		return fmt.Errorf("%w: %s", ErrOutside, dir)
	}
	cur := root
	if rel != "." {
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			cur = filepath.Join(cur, part)
			if IsSymlink(cur) {
				return fmt.Errorf("%w: %s", ErrSymlink, cur)
			}
		}
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		r2, err := filepath.Rel(rootReal, real)
		if err != nil || !(r2 == "." || RelInside(r2)) {
			return fmt.Errorf("%w: %s", ErrOutside, dir)
		}
	}
	return nil
}

func mapRootErr(err error) error {
	// os.Root reports an escape as a *PathError wrapping an "escapes from
	// parent" error; there is no exported sentinel, so match on the text.
	var pe *fs.PathError
	if errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "escapes") {
		return ErrOutside
	}
	return err
}
