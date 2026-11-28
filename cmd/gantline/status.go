package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/justinstimatze/gantline/internal/audit"
	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/queue"
	"github.com/justinstimatze/gantline/screen"
	"github.com/justinstimatze/gantline/tmux"
)

// report is what `gantline status` knows about a pane: the journal's fold,
// plus the screen for what hooks can't say.
type report struct {
	Pane        string           `json:"pane"`
	Key         string           `json:"key"`
	Activity    string           `json:"activity"`       // after reconciling with the screen
	Hooks       journal.Activity `json:"hooks_activity"` // what the journal alone says
	Since       time.Time        `json:"since,omitzero"`
	Model       string           `json:"model,omitempty"`
	ContextPct  float64          `json:"context_pct"`
	HasContext  bool             `json:"has_context"`
	Mode        string           `json:"permission_mode,omitempty"`
	Version     string           `json:"version,omitempty"`
	Compactions int              `json:"compactions"`
	LastError   string           `json:"last_error,omitempty"`
	Busy        bool             `json:"screen_busy"`
	Box         string           `json:"box"` // none, empty, pasted, draft N chars
	Dialog      string           `json:"dialog,omitempty"`
	Options     []screen.Option  `json:"options,omitempty"`
	Spinner     string           `json:"spinner,omitempty"`
	ModeLine    string           `json:"mode_line,omitempty"`
	Note        string           `json:"note,omitempty"`
	Drift       string           `json:"drift,omitempty"`
	Recent      []string         `json:"recent"`
	Queue       []string         `json:"queue,omitempty"`  // items not yet reported, newest last
	Screen      string           `json:"screen,omitempty"` // --raw only
}

// interruptSettle is how long the screen must stay idle while the hooks
// still say working before status calls it an interrupt; Esc fires no hook.
// ccmanager uses the same 1.5s debounce for its state detection.
const interruptSettle = 1500 * time.Millisecond

func statusCmd(s tmux.Server, args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	target := fs.String("pane", "", "pane to report on (default: this pane)")
	asJSON := fs.Bool("json", false, "print the full report as JSON")
	raw := fs.Bool("raw", false, "include the screen text (implies --json)")
	fs.Parse(args)
	id, key, err := resolve(s, *target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gantline status:", err)
		os.Exit(1)
	}
	r, err := buildReport(s, id, key, time.Now(), *raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gantline status:", err)
		os.Exit(1)
	}
	if *asJSON || *raw {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(r)
		return
	}
	fmt.Print(r.brief(time.Now()))
}

func buildReport(s tmux.Server, id, key string, now time.Time, raw bool) (report, error) {
	st, evs, err := journal.Load(key)
	if err != nil {
		return report{}, err
	}
	capture, err := s.Capture(id)
	if err != nil {
		return report{}, err
	}
	r := report{Pane: id, Key: key, Hooks: st.Activity, Since: st.Since, Model: st.Model,
		ContextPct: st.ContextPct, HasContext: st.HasContext, Mode: st.PermissionMode,
		Version: st.Version, Compactions: st.Compactions, LastError: st.LastError}
	r.readScreen(capture)
	if raw {
		r.Screen = capture
	}
	r.reconcile(st.Activity, func() bool {
		time.Sleep(interruptSettle)
		c, err := s.Capture(id)
		return err == nil && !screen.Busy(c)
	})
	r.Drift = drift(st.Version)
	if items, err := queue.Items(key); err == nil {
		for _, it := range items {
			if !it.Reported || !queue.Final(it.State) {
				line := fmt.Sprintf("%s %s %s", it.ID, trunc(it.Cmd, 30), it.State)
				if it.State == queue.Running && it.Detail != "" {
					line += " (" + trunc(it.Detail, 80) + ")"
				}
				r.Queue = append(r.Queue, line)
			}
		}
		if len(r.Queue) > 3 {
			r.Queue = r.Queue[len(r.Queue)-3:]
		}
	}
	for _, e := range lastN(evs, 5) {
		r.Recent = append(r.Recent, fmt.Sprintf("%s %s ago", eventLabel(e), ago(now.Sub(e.Time()))))
	}
	return r, nil
}

// reconcile sets r.Activity from the hooks' activity and the screen, for
// what hooks can't see. Esc (an interrupt, or denying a permission prompt)
// fires no hook, so the journal can say working or awaiting_permission
// after the screen has moved on. stillIdle re-reads the screen after
// interruptSettle and reports whether it is still not busy.
func (r *report) reconcile(hooks journal.Activity, stillIdle func() bool) {
	r.Activity = string(hooks)
	switch {
	case r.Dialog == "permission_dialog":
		r.Activity = string(journal.AwaitingPermission)
	case r.Dialog != "":
		r.Activity = "dialog"
	case (hooks == journal.AwaitingPermission || hooks == journal.Elicitation) && r.Box != "none":
		if r.Busy {
			r.Activity = string(journal.Working)
			r.Note = "the prompt closed and the turn went on; no hook has said so yet"
		} else {
			r.Activity = string(journal.Idle)
			r.Note = "the prompt closed with no hook: denied or interrupted with Esc?"
		}
	case hooks == journal.Working && !r.Busy:
		if stillIdle() {
			r.Activity = string(journal.Idle)
			r.Note = "hooks say working but the screen has been idle 1.5s: interrupted with Esc? (Esc fires no hook)"
		}
	case (hooks == journal.Stopped || hooks == journal.Idle) && r.Busy:
		r.Activity = string(journal.Working)
		r.Note = "screen busy after the last stop: another hook blocked it, or a turn started that no hook reported"
	case hooks == journal.Unknown && r.Busy:
		r.Activity = string(journal.Working)
	case hooks == journal.Unknown:
		r.Activity = string(journal.Idle)
		r.Note = "no hook events for this pane; run `gantline install` for full state"
	}
}

func (r *report) readScreen(capture string) {
	l := screen.Cut(capture)
	r.Busy = screen.Busy(capture)
	r.ModeLine = strings.TrimSpace(l.Text("mode"))
	r.Spinner = l.Text("spinner")
	r.Dialog = screen.Dialog(l)
	if r.Dialog == "" && !l.Dialog.Empty() {
		r.Dialog = "unrecognized"
	}
	if lines, _, _ := l.Region("dialog"); len(lines) > 0 {
		r.Options = screen.Options(lines)
	}
	switch in, found := screen.Input(capture); {
	case !found:
		r.Box = "none"
	case in == "":
		r.Box = "empty"
	case screen.Pasted(in):
		r.Box = "pasted"
	case strings.Contains(in, screen.Hidden):
		r.Box = fmt.Sprintf("draft over %d chars, scrolled", len([]rune(strings.ReplaceAll(in, screen.Hidden, ""))))
	default:
		r.Box = fmt.Sprintf("draft %d chars", len([]rune(in)))
	}
}

// brief is the default output: a few lines, well under 300 tokens (law 7).
func (r report) brief(now time.Time) string {
	var b strings.Builder
	head := r.Activity
	if !r.Since.IsZero() && r.Activity == string(r.Hooks) {
		head += " for " + ago(now.Sub(r.Since))
	}
	fmt.Fprintf(&b, "%s · %s", r.Pane, head)
	if r.Model != "" {
		fmt.Fprintf(&b, " · %s", r.Model)
	}
	if r.HasContext {
		fmt.Fprintf(&b, " · ctx %.0f%%", r.ContextPct)
	}
	if r.Mode != "" {
		fmt.Fprintf(&b, " · mode %s", r.Mode)
	}
	if r.Compactions > 0 {
		fmt.Fprintf(&b, " · compactions %d", r.Compactions)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "screen: %s, box %s", map[bool]string{true: "busy", false: "idle"}[r.Busy], r.Box)
	if r.Spinner != "" {
		fmt.Fprintf(&b, ", %s", trunc(r.Spinner, 60))
	}
	b.WriteString("\n")
	if r.Dialog != "" {
		fmt.Fprintf(&b, "dialog: %s", r.Dialog)
		for _, o := range r.Options {
			mark := ""
			if o.Selected {
				mark = "*"
			}
			fmt.Fprintf(&b, " [%d. %s%s]", o.N, trunc(o.Label, 30), mark)
		}
		b.WriteString("\n")
	}
	if r.LastError != "" {
		fmt.Fprintf(&b, "last error: %s\n", r.LastError)
	}
	if r.Drift != "" {
		fmt.Fprintf(&b, "drift: %s\n", r.Drift)
	}
	if r.Note != "" {
		fmt.Fprintf(&b, "note: %s\n", r.Note)
	}
	if len(r.Queue) > 0 {
		fmt.Fprintf(&b, "queue: %s\n", strings.Join(r.Queue, "; "))
	}
	if len(r.Recent) > 0 {
		fmt.Fprintf(&b, "recent: %s\n", strings.Join(r.Recent, ", "))
	}
	return b.String()
}

func eventLabel(e journal.Event) string {
	for _, extra := range []string{e.Source, e.Trigger, e.NotificationType, e.ToolName} {
		if extra != "" {
			return e.Ev + "(" + extra + ")"
		}
	}
	return e.Ev
}

func lastN(evs []journal.Event, n int) []journal.Event {
	if len(evs) > n {
		evs = evs[len(evs)-n:]
	}
	out := slices.Clone(evs)
	slices.Reverse(out)
	return out
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// drift says when the running Claude Code is neither a version gantline
// shipped checked against nor one with local captures: its screens were
// never checked against the fingerprints, so recognised dialogs and the
// busy line may be read wrong.
func drift(version string) string {
	if version == "" || slices.Contains(screen.CheckedVersions, version) {
		return ""
	}
	dir := filepath.Join(audit.StateDir(), "corpus")
	if _, err := os.Stat(filepath.Join(dir, version)); err == nil {
		return ""
	}
	have := slices.Clone(screen.CheckedVersions)
	local, _ := filepath.Glob(filepath.Join(dir, "*"))
	for _, h := range local {
		if b := filepath.Base(h); !slices.Contains(have, b) {
			have = append(have, b)
		}
	}
	return fmt.Sprintf("Claude Code %s hasn't been checked (checked: %s); screen reads may be wrong", version, strings.Join(have, ", "))
}
