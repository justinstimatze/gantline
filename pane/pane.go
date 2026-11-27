// Package pane finds the tmux pane a Claude Code session is running in.
package pane

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/justinstimatze/gantline/tmux"
)

// Pane is a tmux pane with a Claude Code process somewhere below it.
type Pane struct {
	ID        string // tmux pane id, e.g. "%5"
	PanePID   int    // the pane's own process (usually a shell)
	ClaudePID int    // the claude process found below it
	Cwd       string // the claude process's working directory
}

// Self returns the pane id this process runs in, from $TMUX_PANE, or "".
func Self() string { return os.Getenv("TMUX_PANE") }

// List returns every pane on the server with a claude process below it.
func List(s tmux.Server) ([]Pane, error) {
	out, err := s.Run("list-panes", "-aF", "#{pane_id} #{pane_pid}")
	if err != nil {
		return nil, err
	}
	var panes []Pane
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		cpid := findClaude(pid)
		if cpid == 0 {
			continue
		}
		cwd, _ := os.Readlink(fmt.Sprintf("/proc/%d/cwd", cpid))
		panes = append(panes, Pane{ID: f[0], PanePID: pid, ClaudePID: cpid, Cwd: cwd})
	}
	return panes, nil
}

// ByCwd returns the id of the first pane whose claude process runs in cwd,
// or "" if there is none.
func ByCwd(s tmux.Server, cwd string) string {
	if cwd == "" {
		return ""
	}
	panes, err := List(s)
	if err != nil {
		return ""
	}
	for _, p := range panes {
		if p.Cwd == cwd {
			return p.ID
		}
	}
	return ""
}

// findClaude walks the descendants of root breadth-first and returns the pid
// of the first whose argv has a "claude" entry, or 0. It visits at most 200
// processes.
func findClaude(root int) int {
	queue := []int{root}
	seen := make(map[int]bool, 32)
	for len(queue) > 0 && len(seen) < 200 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if isClaude(cur) {
			return cur
		}
		queue = append(queue, children(cur)...)
	}
	return 0
}

func isClaude(pid int) bool {
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	for _, a := range strings.Split(string(cmdline), "\x00") {
		if a == "claude" || strings.HasSuffix(a, "/claude") {
			return true
		}
	}
	return false
}

func children(pid int) []int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
	if err != nil {
		return nil
	}
	var out []int
	for _, f := range strings.Fields(string(data)) {
		if n, err := strconv.Atoi(f); err == nil {
			out = append(out, n)
		}
	}
	return out
}
