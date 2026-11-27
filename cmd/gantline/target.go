package main

import (
	"fmt"
	"strings"

	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/pane"
	"github.com/justinstimatze/gantline/tmux"
)

// resolve turns a pane target ("", "self", "%3", "sess:1.0") into its pane
// id and journal key. The key includes the server's socket path, so it
// matches what `gantline hook` computes from inside that pane.
func resolve(s tmux.Server, target string) (id, key string, err error) {
	if target == "" || target == "self" {
		target = pane.Self()
	}
	if target == "" {
		return "", "", fmt.Errorf("not in tmux and no --pane given")
	}
	out, err := s.Run("display-message", "-p", "-t", target, "#{pane_id} #{socket_path}")
	if err != nil {
		return "", "", fmt.Errorf("no pane %s: %w", target, err)
	}
	// Outside a tmux client, display-message exits 0 for a target that
	// matches nothing and prints an empty pane id.
	id, sock, _ := strings.Cut(strings.TrimRight(out, "\n"), " ")
	if !strings.HasPrefix(id, "%") || sock == "" {
		return "", "", fmt.Errorf("no pane %s", target)
	}
	return id, journal.Key(sock, id), nil
}
