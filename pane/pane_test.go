package pane

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/justinstimatze/gantline/tmux"
)

// TestListFindsClaude runs a stand-in script named "claude" under a shell in
// a throwaway tmux server and checks that List finds it and its cwd.
func TestListFindsClaude(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	s := tmux.Server{Socket: fmt.Sprintf("gantline-pane-test-%d", os.Getpid())}
	if _, err := s.Run("new-session", "-d", "-c", work, "sh", "-c", fake+"; sleep 30"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Run("kill-server") })

	var panes []Pane
	for range 40 {
		var err error
		panes, err = List(s)
		if err != nil {
			t.Fatal(err)
		}
		if len(panes) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(panes) != 1 {
		t.Fatalf("List found %d panes, want 1: %+v", len(panes), panes)
	}
	want, _ := filepath.EvalSymlinks(work)
	if panes[0].Cwd != want {
		t.Errorf("Cwd = %q, want %q", panes[0].Cwd, want)
	}
	if got := ByCwd(s, want); got != panes[0].ID {
		t.Errorf("ByCwd = %q, want %q", got, panes[0].ID)
	}
}
