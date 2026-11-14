package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadHeadReadsPrefixWithoutSizeCap(t *testing.T) {
	root := t.TempDir()
	big := "#!/usr/bin/env python3\n" + strings.Repeat("x", 9<<20) // over MaxFileBytes
	if err := os.WriteFile(filepath.Join(root, "tool"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	head, err := ReadHead(root, "tool", 64)
	if err != nil || len(head) != 64 || !strings.HasPrefix(string(head), "#!/usr/bin/env python3") {
		t.Fatalf("ReadHead = %q, %v", head, err)
	}
	if _, err := ReadRegular(root, "tool", 0); !errors.Is(err, ErrTooLarge) {
		t.Errorf("ReadRegular must still refuse the oversize file, got %v", err)
	}
}

func TestReadHeadRefusesSymlinkAndEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("#!/usr/bin/env python3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "leak")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHead(root, "leak", 64); !errors.Is(err, ErrSymlink) {
		t.Errorf("symlink: got %v, want ErrSymlink", err)
	}
	if _, err := ReadHead(root, "../x", 64); !errors.Is(err, ErrOutside) {
		t.Errorf("escape: got %v, want ErrOutside", err)
	}
}
