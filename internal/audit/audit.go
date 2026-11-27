// Package audit appends one JSON line per write gantline makes to a pane, to
// $XDG_STATE_HOME/gantline/say.jsonl (default ~/.local/state). It records
// sizes, timings and outcomes, never the text typed. A failure to write the
// log never fails the operation being logged.
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/justinstimatze/gantline/journal"
)

// StateDir is $XDG_STATE_HOME/gantline, created if missing.
func StateDir() string {
	dir := journal.StateDir()
	os.MkdirAll(dir, 0o700)
	os.Chmod(dir, 0o700) // tighten a directory made with looser modes
	return dir
}

// Rec is one audit line. Extra holds optional fields such as a queue id or
// an outcome.
type Rec struct {
	Kind   string // "say" or "q"
	Pane   string
	Bytes  int
	Enters int
	Took   time.Duration
	Err    error
	Extra  map[string]any
}

// Write appends r to say.jsonl.
func Write(r Rec) {
	rec := map[string]any{
		"time":   time.Now().Format(time.RFC3339),
		"kind":   r.Kind,
		"pane":   r.Pane,
		"socket": os.Getenv("GANTLINE_TMUX_SOCKET"),
		"bytes":  r.Bytes,
		"enters": r.Enters,
		"ms":     r.Took.Milliseconds(),
		"ok":     r.Err == nil,
	}
	if r.Err != nil {
		rec["error"] = r.Err.Error()
	}
	for k, v := range r.Extra {
		rec[k] = v
	}
	line, _ := json.Marshal(rec)
	f, err := os.OpenFile(filepath.Join(StateDir(), "say.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}
