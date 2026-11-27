package journal

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Activity is what the hooks last said the session was doing.
type Activity string

const (
	Unknown            Activity = "unknown"
	Working            Activity = "working"
	Stopped            Activity = "stopped" // a Stop fired; another hook may still block it, so confirm on screen
	Idle               Activity = "idle"    // session start, /clear, a finished /compact, or an idle_prompt
	AwaitingPermission Activity = "awaiting_permission"
	Elicitation        Activity = "elicitation"
	Compacting         Activity = "compacting"
	Ended              Activity = "ended"
)

// State is the fold of a pane's events and latest statusline snapshot.
type State struct {
	Activity       Activity
	Since          time.Time // when Activity last changed
	LastEvent      time.Time
	SessionID      string
	Transcript     string
	PermissionMode string
	Model          string // model id
	ContextPct     float64
	HasContext     bool
	Version        string
	Compactions    int
	LastStop       time.Time
	LastError      string // from the last StopFailure
	Events         int
}

// Fold computes the state after evs, in order, plus st (zero if none).
// A hook that fires nothing (an Esc interrupt) leaves the fold showing the
// last state it saw; callers reconcile with the screen.
func Fold(evs []Event, st Status) State {
	s := State{Activity: Unknown}
	set := func(a Activity, t time.Time) {
		if s.Activity != a {
			s.Activity, s.Since = a, t
		}
	}
	beforeCompact := Unknown
	var modelAt time.Time
	for _, e := range evs {
		t := e.Time()
		s.LastEvent, s.Events = t, s.Events+1
		if e.Sid != "" {
			s.SessionID = e.Sid
		}
		if e.Transcript != "" {
			s.Transcript = e.Transcript
		}
		if e.PermissionMode != "" {
			s.PermissionMode = e.PermissionMode
		}
		switch e.Ev {
		case "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "SubagentStart":
			set(Working, t)
		case "PermissionRequest":
			set(AwaitingPermission, t)
		case "Notification":
			switch e.NotificationType {
			case "permission_prompt":
				set(AwaitingPermission, t)
			case "idle_prompt":
				set(Idle, t)
			case "elicitation_dialog", "elicitation_url_dialog":
				set(Elicitation, t)
			case "elicitation_complete", "elicitation_response":
				set(Working, t)
			}
		case "PreCompact":
			if s.Activity != Compacting {
				beforeCompact = s.Activity
			}
			set(Compacting, t)
		case "PostCompact":
			s.Compactions++
			if e.Trigger == "auto" && beforeCompact == Working {
				set(Working, t)
			} else {
				set(Idle, t)
			}
		case "SessionStart":
			if e.Model != "" {
				s.Model, modelAt = e.Model, t
			}
			if e.Source != "compact" {
				set(Idle, t)
			}
		case "PostModelSwitch":
			if e.ToModel != "" {
				s.Model, modelAt = e.ToModel, t
			}
		case "Stop":
			s.LastStop = t
			set(Stopped, t)
		case "StopFailure":
			s.LastStop, s.LastError = t, e.Error
			set(Stopped, t)
		case "SessionEnd":
			set(Ended, t)
		}
	}
	if st.T != 0 {
		s.Version = st.Version
		s.ContextPct, s.HasContext = st.ContextPct, st.HasContext
		if st.ModelID != "" && time.Unix(0, st.T).After(modelAt) {
			s.Model = st.ModelID
		}
	}
	return s
}

// Load folds key's journal and status snapshot.
func Load(key string) (State, []Event, error) {
	evs, err := Events(key)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{}, nil, err
	}
	st, _ := ReadStatus(key)
	return Fold(evs, st), evs, nil
}

// After returns the events recorded after t.
func After(evs []Event, t time.Time) []Event {
	for i, e := range evs {
		if e.Time().After(t) {
			return evs[i:]
		}
	}
	return nil
}

// ErrTimeout means the awaited event was not recorded in time.
var ErrTimeout = errors.New("journal: timed out")

// WaitOptions tunes the waits. Zero fields take the defaults.
type WaitOptions struct {
	Timeout time.Duration // default 30m
	Quiet   time.Duration // WaitTurnEnd: transcript unchanged this long; default 3s
	Poll    time.Duration // default 250ms
}

func (o WaitOptions) withDefaults() WaitOptions {
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Minute
	}
	if o.Quiet <= 0 {
		o.Quiet = 3 * time.Second
	}
	if o.Poll <= 0 {
		o.Poll = 250 * time.Millisecond
	}
	return o
}

// WaitFor blocks until an event after since satisfies match, and returns it.
func WaitFor(key string, since time.Time, opt WaitOptions, match func(Event) bool) (Event, error) {
	opt = opt.withDefaults()
	for deadline := time.Now().Add(opt.Timeout); time.Now().Before(deadline); time.Sleep(opt.Poll) {
		evs, _ := Events(key)
		for _, e := range After(evs, since) {
			if match(e) {
				return e, nil
			}
		}
	}
	return Event{}, ErrTimeout
}

// WaitTurnEnd blocks until a turn ends after since: the fold of the events
// after since is Stopped or Idle, and the transcript has been quiet for
// opt.Quiet. A quiet transcript is not proof the session is idle (a model
// thinking after a blocked Stop writes nothing for a while), so callers
// about to type must also check the screen.
func WaitTurnEnd(key string, since time.Time, opt WaitOptions) error {
	opt = opt.withDefaults()
	for deadline := time.Now().Add(opt.Timeout); time.Now().Before(deadline); time.Sleep(opt.Poll) {
		evs, _ := Events(key)
		later := After(evs, since)
		if len(later) == 0 {
			continue
		}
		s := Fold(later, Status{})
		if s.Activity == Stopped && quiet(s.Transcript, s.LastStop, opt.Quiet) {
			return nil
		}
	}
	return ErrTimeout
}

// quiet reports whether the transcript has not changed for d, judged from
// the later of its mtime and since. With no transcript it falls back to d
// having passed since since.
func quiet(transcript string, since time.Time, d time.Duration) bool {
	last := since
	if transcript != "" {
		if fi, err := os.Stat(transcript); err == nil && fi.ModTime().After(last) {
			last = fi.ModTime()
		}
	}
	return time.Since(last) >= d
}

// staleCompaction bounds CompactionOpen: an Esc during a compaction may
// leave a PreCompact with no PostCompact, and that must not hold the pane
// forever. Real compactions on 2.1.332 took 35 s to 2 min.
const staleCompaction = 5 * time.Minute

// CompactionOpen reports whether a compaction started (PreCompact) and has
// not finished (PostCompact) as of now, within staleCompaction. Activity
// can't tell: tool events from background work arrive mid-compaction and
// set it to working.
func CompactionOpen(evs []Event, now time.Time) bool {
	var pre, post time.Time
	for _, e := range evs {
		switch e.Ev {
		case "PreCompact":
			pre = e.Time()
		case "PostCompact":
			post = e.Time()
		}
	}
	return pre.After(post) && now.Sub(pre) < staleCompaction
}

// BusyAt lists the reasons the journal gives for the session being busy
// at t, from events up to t; none means it looked idle. A reason is
// evidence, not proof: an Esc interrupt fires no hook, so a turn it ended
// still reads as running.
func BusyAt(evs []Event, t time.Time) []string {
	var before []Event
	for _, e := range evs {
		if e.Time().After(t) {
			break
		}
		before = append(before, e)
	}
	var reasons []string
	var turn, end, lastTool time.Time
	for _, e := range before {
		switch e.Ev {
		case "UserPromptSubmit":
			turn = e.Time()
		case "Stop", "StopFailure":
			end = e.Time()
		case "SessionStart":
			turn, end = time.Time{}, e.Time()
		case "PostToolUse", "PostToolUseFailure", "PreToolUse":
			lastTool = e.Time()
		}
	}
	if turn.After(end) {
		reasons = append(reasons, fmt.Sprintf("a turn was running (UserPromptSubmit %s before, no Stop since)", t.Sub(turn).Round(time.Second)))
	}
	if !end.IsZero() && lastTool.After(end) && t.Sub(lastTool) < 10*time.Second {
		reasons = append(reasons, fmt.Sprintf("a tool finished %s before, after the last Stop (a blocked Stop or a background agent)", t.Sub(lastTool).Round(time.Millisecond)))
	}
	if CompactionOpen(before, t) {
		reasons = append(reasons, "a compaction was open")
	}
	return reasons
}
