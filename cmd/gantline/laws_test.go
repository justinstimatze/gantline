package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Law 9: real captured screens stay local and are never committed. Fails
// if git tracks any path under a corpus directory, or any testdata file
// holds this machine's home directory (a sign it was copied from a real
// capture rather than written by hand).
func TestNoRealCapturesInGit(t *testing.T) {
	out, err := exec.Command("git", "-C", "../..", "ls-files").Output()
	if err != nil {
		t.Skip("not a git checkout")
	}
	home, _ := os.UserHomeDir()
	for _, f := range strings.Fields(string(out)) {
		if strings.Contains(f, "corpus") {
			t.Errorf("tracked file under a corpus path: %s", f)
		}
		if !strings.Contains(f, "testdata/") || home == "" {
			continue
		}
		if data, err := os.ReadFile("../../" + f); err == nil && strings.Contains(string(data), home) {
			t.Errorf("testdata file %s contains %s: real capture?", f, home)
		}
	}
}
