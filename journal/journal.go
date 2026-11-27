// Package journal records what Claude Code's hooks and statusline report
// about one tmux pane, and folds those records into the pane's state.
//
// `gantline hook <Event>` appends one minimal Event per hook call to
// <Dir>/<key>/events.jsonl, and `gantline statusline` keeps the latest
// statusline snapshot in <Dir>/<key>/status.json. The key names the pane
// (tmux socket path plus pane id), so it survives /clear and /compact,
// which change the session id.
//
// The journal never stores prompt text, assistant messages, tool inputs or
// compaction summaries: only event names, times, session ids, the
// transcript path, and a whitelist of short fields such as the compaction
// trigger and a hook's error message.
//
// Facts this relies on (Claude Code 2.1.332, see
// docs/claude-code-behavior.md): a Stop can be followed by more of the same
// turn when another Stop hook blocks it; Esc interrupts fire no hook; the
// /compact order is PreCompact, SessionStart(compact), PostCompact.
package journal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StateDir is gantline's state directory: $XDG_STATE_HOME/gantline, else
// ~/.local/state/gantline. With neither variable set it falls back to a
// per-user directory under the temp dir, never one relative to the cwd.
func StateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
			base = filepath.Join(home, ".local", "state")
		} else {
			base = filepath.Join(os.TempDir(), fmt.Sprintf("gantline-state-%d", os.Getuid()))
		}
	}
	return filepath.Join(base, "gantline")
}

// Dir is where pane journals live: StateDir()/panes.
func Dir() string { return filepath.Join(StateDir(), "panes") }

// Key names a pane uniquely across tmux servers: pane ids such as "%0"
// repeat on every server, so the server's socket path is part of the key.
func Key(socketPath, pane string) string {
	r := strings.NewReplacer("/", "_", "%", "")
	return r.Replace(socketPath) + "__" + r.Replace(pane)
}

// KeyFromEnv builds the key for the pane this process runs in, from $TMUX
// ("socket,pid,session") and $TMUX_PANE. It returns "" outside tmux.
func KeyFromEnv() string {
	tmuxEnv, pane := os.Getenv("TMUX"), os.Getenv("TMUX_PANE")
	if tmuxEnv == "" || pane == "" {
		return ""
	}
	socket, _, _ := strings.Cut(tmuxEnv, ",")
	return Key(socket, pane)
}

// PaneDir is the directory holding one pane's journal, queue and locks.
func PaneDir(key string) string { return filepath.Join(Dir(), key) }

// Event is one hook call, reduced to the whitelisted fields.
type Event struct {
	T                int64  `json:"t"` // unix nanoseconds when recorded
	Ev               string `json:"ev"`
	Sid              string `json:"sid,omitempty"`
	Transcript       string `json:"transcript,omitempty"`
	PermissionMode   string `json:"mode,omitempty"`
	Trigger          string `json:"trigger,omitempty"`
	Source           string `json:"source,omitempty"`
	NotificationType string `json:"ntype,omitempty"`
	ToolName         string `json:"tool,omitempty"`
	Model            string `json:"model,omitempty"`
	FromModel        string `json:"from_model,omitempty"`
	ToModel          string `json:"to_model,omitempty"`
	RequestedModel   string `json:"requested_model,omitempty"`
	Error            string `json:"error,omitempty"`
	StopHookActive   bool   `json:"stop_active,omitempty"`
	HasInstructions  bool   `json:"has_instructions,omitempty"`
	SummaryLen       int    `json:"summary_len,omitempty"`
	AgentID          string `json:"agent_id,omitempty"`
}

// Time returns when the event was recorded.
func (e Event) Time() time.Time { return time.Unix(0, e.T) }

// FromHook reduces a hook's stdin JSON to an Event. name is used when the
// payload has no hook_event_name.
func FromHook(raw []byte, name string, now time.Time) (Event, error) {
	var in struct {
		Event              string  `json:"hook_event_name"`
		SessionID          string  `json:"session_id"`
		TranscriptPath     string  `json:"transcript_path"`
		PermissionMode     string  `json:"permission_mode"`
		Trigger            string  `json:"trigger"`
		Source             string  `json:"source"`
		NotificationType   string  `json:"notification_type"`
		ToolName           string  `json:"tool_name"`
		Model              string  `json:"model"`
		FromModel          string  `json:"from_model"`
		ToModel            string  `json:"to_model"`
		RequestedModel     string  `json:"requested_model"`
		Error              string  `json:"error"`
		StopHookActive     bool    `json:"stop_hook_active"`
		CustomInstructions *string `json:"custom_instructions"`
		CompactSummary     string  `json:"compact_summary"`
		AgentID            string  `json:"agent_id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil && len(raw) > 0 {
		return Event{}, fmt.Errorf("journal: hook payload: %w", err)
	}
	ev := in.Event
	if ev == "" {
		ev = canonical(name)
	}
	if ev == "" {
		return Event{}, errors.New("journal: no event name")
	}
	return Event{
		T: now.UnixNano(), Ev: ev, Sid: in.SessionID, Transcript: in.TranscriptPath,
		PermissionMode: in.PermissionMode, Trigger: in.Trigger, Source: in.Source,
		NotificationType: in.NotificationType, ToolName: in.ToolName, Model: in.Model,
		FromModel: in.FromModel, ToModel: in.ToModel, RequestedModel: in.RequestedModel,
		Error: in.Error, StopHookActive: in.StopHookActive,
		HasInstructions: in.CustomInstructions != nil && *in.CustomInstructions != "",
		SummaryLen:      len(in.CompactSummary), AgentID: in.AgentID,
	}, nil
}

// canonical maps a command-line event name to its hook name, accepting the
// old lowercase `gantline hook stop` form.
func canonical(name string) string {
	if name == "stop" {
		return "Stop"
	}
	return name
}

// MaxBytes is the size at which events.jsonl rotates to events.1.jsonl.
const MaxBytes = 1 << 20

// Append adds ev to key's journal. Lines are small, so O_APPEND writes from
// concurrent hooks don't interleave.
func Append(key string, ev Event) error {
	if key == "" {
		return errors.New("journal: no key")
	}
	dir := PaneDir(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "events.jsonl")
	if fi, err := os.Stat(path); err == nil && fi.Size() > MaxBytes {
		os.Rename(path, filepath.Join(dir, "events.1.jsonl"))
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// Events returns key's events in order, oldest first, including the rotated
// file. Unparseable lines are skipped.
func Events(key string) ([]Event, error) {
	var out []Event
	found := false
	for _, name := range []string{"events.1.jsonl", "events.jsonl"} {
		f, err := os.Open(filepath.Join(PaneDir(key), name))
		if err != nil {
			continue
		}
		found = true
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e Event
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.Ev != "" {
				out = append(out, e)
			}
		}
		f.Close()
	}
	if !found {
		return nil, os.ErrNotExist
	}
	// Async hooks run as separate processes and can append out of order:
	// sort by when each event happened, not when it was written.
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out, nil
}

// Status is the latest statusline snapshot for a pane.
type Status struct {
	T          int64   `json:"t"`
	SessionID  string  `json:"sid,omitempty"`
	ModelID    string  `json:"model_id,omitempty"`
	ModelName  string  `json:"model_name,omitempty"`
	ContextPct float64 `json:"ctx_pct"`
	HasContext bool    `json:"has_ctx"`
	Version    string  `json:"version,omitempty"`
}

// StatusFromStatusline reduces the statusline's stdin JSON to a Status.
func StatusFromStatusline(raw []byte, now time.Time) (Status, error) {
	var in struct {
		SessionID string `json:"session_id"`
		Version   string `json:"version"`
		Model     struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"model"`
		ContextWindow struct {
			UsedPercentage *float64 `json:"used_percentage"`
		} `json:"context_window"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return Status{}, fmt.Errorf("journal: statusline payload: %w", err)
	}
	st := Status{T: now.UnixNano(), SessionID: in.SessionID, ModelID: in.Model.ID,
		ModelName: in.Model.DisplayName, Version: in.Version}
	if p := in.ContextWindow.UsedPercentage; p != nil {
		st.ContextPct, st.HasContext = *p, true
	}
	return st, nil
}

// WriteStatus replaces key's status.json atomically.
func WriteStatus(key string, st Status) error {
	if key == "" {
		return errors.New("journal: no key")
	}
	dir := PaneDir(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "status-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	return os.Rename(tmp.Name(), filepath.Join(dir, "status.json"))
}

// ReadStatus returns key's latest statusline snapshot.
func ReadStatus(key string) (Status, error) {
	var st Status
	data, err := os.ReadFile(filepath.Join(PaneDir(key), "status.json"))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(data, &st)
}
