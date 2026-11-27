package relaunch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Session is what a relaunch needs from the running claude process.
type Session struct {
	PID  int
	Argv []string // /proc/<pid>/cmdline
	Dir  string   // /proc/<pid>/cwd
	Env  []string // /proc/<pid>/environ: what its launcher set
	ID   string   // sessionId from ~/.claude/sessions/<pid>.json
}

// ErrNoClaude means no single claude process runs in the pane.
var ErrNoClaude = errors.New("relaunch: no claude process in the pane")

// Find returns the claude process at or under the pane's own process
// (a session manager may exec claude in the pane; a shell pane runs it as a child), read from
// /proc.
func Find(panePID int) (Session, error) {
	pid, err := findClaude(panePID)
	if err != nil {
		return Session{}, err
	}
	s := Session{PID: pid}
	if s.Argv, err = nulList(fmt.Sprintf("/proc/%d/cmdline", pid)); err != nil {
		return s, err
	}
	if s.Env, err = nulList(fmt.Sprintf("/proc/%d/environ", pid)); err != nil {
		return s, err
	}
	if s.Dir, err = os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err != nil {
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

func findClaude(pid int) (int, error) {
	if comm(pid) == "claude" {
		return pid, nil
	}
	var found []int
	var walk func(int, int)
	walk = func(p, depth int) {
		if depth > 4 {
			return
		}
		for _, c := range children(p) {
			if comm(c) == "claude" {
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

func comm(pid int) string {
	b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	return strings.TrimSpace(string(b))
}

func children(pid int) []int {
	tasks, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
	var out []int
	for _, t := range tasks {
		b, _ := os.ReadFile(t)
		for _, f := range strings.Fields(string(b)) {
			if n, err := strconv.Atoi(f); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

func nulList(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		return nil, nil
	}
	return strings.Split(string(b), "\x00"), nil
}
