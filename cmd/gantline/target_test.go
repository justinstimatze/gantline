package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/tmux"
)

// A target that matches no pane is an error, never a queue for a pane that
// doesn't exist. Outside a tmux client, display-message exits 0 for such a
// target and prints an empty pane id.
func TestResolveRefusesMissingPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	s := tmux.Server{Socket: fmt.Sprintf("gantline-target-%d-%d", os.Getpid(), time.Now().UnixNano())}
	if _, err := s.Run("new-session", "-d", "sleep 60"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Run("kill-server") })
	out, err := s.Run("display-message", "-p", "-t", "%0", "#{pane_id} #{socket_path}")
	if err != nil {
		t.Fatal(err)
	}
	want, sock, _ := strings.Cut(strings.TrimSpace(out), " ")

	id, key, err := resolve(s, want)
	if err != nil || id != want || key != journal.Key(sock, want) {
		t.Fatalf("resolve(%s) = %q, %q, %v", want, id, key, err)
	}
	for _, bad := range []string{"nosuch", "%999", "nosuch:1.0"} {
		if id, key, err := resolve(s, bad); err == nil {
			t.Errorf("resolve(%q) = %q, %q, want an error", bad, id, key)
		}
	}
	if _, _, err := resolve(s, ""); err == nil {
		t.Error("resolve(\"\") outside tmux succeeded")
	}
}
