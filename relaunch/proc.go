package relaunch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/justinstimatze/gantline/internal/proc"
)

// Session is what a relaunch needs from the running claude process.
type Session struct {
	PID  int
	Argv []string // its command line
	Dir  string   // its working directory
	Env  []string // the environment its launcher set
	ID   string   // sessionId from ~/.claude/sessions/<pid>.json
}

// ErrNoClaude means no single claude process runs in the pane.
var ErrNoClaude = errors.New("relaunch: no claude process in the pane")

// Find returns the claude process at or under the pane's own process
// (a session manager may exec claude in the pane; a shell pane runs it as a child).
func Find(panePID int) (Session, error) {
	tree, err := proc.Snapshot()
	if err != nil {
		return Session{}, err
	}
	pid, err := findClaude(tree, panePID)
	if err != nil {
		return Session{}, err
	}
	s := Session{PID: pid}
	if s.Argv, err = proc.Argv(pid); err != nil {
		return s, err
	}
	if s.Env, err = proc.Env(pid); err != nil {
		return s, err
	}
	if s.Dir, err = proc.Cwd(pid); err != nil {
		return s, err
	}
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".claude", "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return s, fmt.Errorf("relaunch: claude pid %d has no session record: %w", pid, err)
	}
	var rec struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(data, &rec) != nil || rec.SessionID == "" {
		return s, fmt.Errorf("relaunch: claude pid %d: session record has no sessionId", pid)
	}
	s.ID = rec.SessionID
	return s, nil
}

func findClaude(tree proc.Tree, pid int) (int, error) {
	if isClaude(pid) {
		return pid, nil
	}
	var found []int
	var walk func(int, int)
	walk = func(p, depth int) {
		if depth > 4 {
			return
		}
		for _, c := range tree[p] {
			if isClaude(c) {
				found = append(found, c)
				continue
			}
			walk(c, depth+1)
		}
	}
	walk(pid, 0)
	switch len(found) {
	case 0:
		return 0, ErrNoClaude
	case 1:
		return found[0], nil
	}
	return 0, fmt.Errorf("%w: %d claude processes under pid %d", ErrNoClaude, len(found), pid)
}

// isClaude matches the native binary by name: "claude" from the native
// installer, "claude.exe" from npm, whose bin entry is bin/claude.exe on
// every platform. On macOS the process takes that name ("claude.exe").
func isClaude(pid int) bool {
	n := proc.Name(pid)
	return n == "claude" || n == "claude.exe"
}
