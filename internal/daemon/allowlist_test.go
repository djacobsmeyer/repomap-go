package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootAllowedEmptyAllowsAll(t *testing.T) {
	ok, err := RootAllowed(nil, t.TempDir())
	if !ok || err != nil {
		t.Fatalf("empty allowlist must allow: %v %v", ok, err)
	}
}

func TestRootAllowed(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "wt")
	inside := filepath.Join(allowed, "issue-1")
	sibling := filepath.Join(base, "wt-evil") // shares the prefix string
	secret := filepath.Join(base, "secrets")
	for _, d := range []string{inside, sibling, secret} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(allowed, "link-out")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	list := []string{allowed}
	cases := []struct {
		path string
		want bool
	}{
		{allowed, true},
		{inside, true},
		{sibling, false},
		{secret, false},
		{link, false}, // inside lexically, outside really
		{filepath.Join(allowed, "issue-1", "..", "..", "secrets"), false},
	}
	for _, c := range cases {
		got, _ := RootAllowed(list, c.path)
		if got != c.want {
			t.Errorf("RootAllowed(%s) = %v, want %v", c.path, got, c.want)
		}
	}
	if ok, err := RootAllowed(list, filepath.Join(base, "missing")); ok || err == nil {
		t.Errorf("unresolvable path must be refused with an error: %v %v", ok, err)
	}
}

func TestParseRootList(t *testing.T) {
	got := ParseRootList(" /a: :/b/c:")
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b/c" {
		t.Fatalf("got %v", got)
	}
}

func TestAddProjectRefusesOutsideAllowlist(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "ok")
	other := filepath.Join(base, "other")
	for _, d := range []string{allowed, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := New(Options{AllowedRoots: []string{allowed}})
	if err := d.AddProject(t.Context(), other); err == nil {
		t.Fatal("AddProject registered a path outside the allowlist")
	}
	resp := d.dispatch(t.Context(), Message{Type: "mcp_connect", Payload: []byte(`{"path":"` + other + `"}`)})
	if resp.OK {
		t.Fatal("mcp_connect auto-registered a path outside the allowlist")
	}
	if n := len(d.List()); n != 0 {
		t.Fatalf("%d projects registered", n)
	}
}

func TestEnsurePrivateDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "socks")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(open, 0o777)
	if err := EnsurePrivateDir(open); err == nil {
		t.Fatal("world-writable dir accepted")
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(link); err == nil {
		t.Fatal("symlinked dir accepted")
	}
	if err := EnsurePrivateDir("relative"); err == nil {
		t.Fatal("relative dir accepted")
	}
}
