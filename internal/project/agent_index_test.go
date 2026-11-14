package project

import (
	"os"
	"path/filepath"
	"testing"
)

// IndexSnapshot: extensionless python scripts are parsed, every other
// regular file is listed (for the agent tools' text grep), and symlinks are
// neither.
func TestIndexSnapshotParsesScriptsAndListsOtherFiles(t *testing.T) {
	p, root, outside := symlinkTree(t, func(root, outside string) {
		mustWrite(t, filepath.Join(root, "pkg", "agent-queue"), "#!/usr/bin/env python3\ndef cmd_add():\n    pass\n")
		mustWrite(t, filepath.Join(root, "pkg", "run.sh"), "#!/bin/sh\necho real_fn\n")
		if err := os.Symlink(filepath.Join(outside, "secret.py"), filepath.Join(root, "pkg", "leak")); err != nil {
			t.Fatal(err)
		}
	})
	_ = outside
	tags, _, files := p.IndexSnapshot()
	if _, ok := tags["pkg/agent-queue"]; !ok {
		t.Errorf("extensionless python script not indexed; tags for %v", keys(tags))
	}
	if _, ok := files["pkg/run.sh"]; !ok {
		t.Errorf("unparsed shell script missing from Files: %v", keys(files))
	}
	if _, ok := tags["pkg/run.sh"]; ok {
		t.Error("a shell script must not be parsed")
	}
	if _, ok := files["pkg/leak"]; ok {
		t.Error("a symlink must not be listed")
	}
	if b, err := p.ReadSource("pkg/leak"); err == nil {
		t.Errorf("ReadSource followed a symlink: %q", b)
	}

	// Watcher path: a new file appears in Files, a deleted one leaves it.
	mustWrite(t, filepath.Join(root, "pkg", "notes.txt"), "hello\n")
	if err := os.Remove(filepath.Join(root, "pkg", "run.sh")); err != nil {
		t.Fatal(err)
	}
	p.handleChanges([]string{"pkg/notes.txt", "pkg/run.sh"})
	_, _, files = p.IndexSnapshot()
	if _, ok := files["pkg/notes.txt"]; !ok {
		t.Error("handleChanges did not add notes.txt to Files")
	}
	if _, ok := files["pkg/run.sh"]; ok {
		t.Error("handleChanges did not drop the deleted run.sh from Files")
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
