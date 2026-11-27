package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gantline/internal/audit"
	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/screen"
)

// Law 7: the default status output stays brief. A report with every field
// at its longest must fit in 1200 bytes (about 300 tokens).
func TestStatusBriefIsBrief(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("x", 500)
	r := report{Pane: "%12", Activity: "awaiting_permission", Hooks: "awaiting_permission", Since: now.Add(-time.Hour),
		Model: "claude-sonnet-5-5[1m]", ContextPct: 99, HasContext: true, Mode: "bypassPermissions", Compactions: 12,
		LastError: "rate_limit", Busy: true, Box: "draft 4000 chars", Dialog: "permission_dialog", Spinner: long,
		Options: []screen.Option{{N: 1, Label: long, Selected: true}, {N: 2, Label: long}, {N: 3, Label: long}},
		Note:    "hooks say working but the screen has been idle 1.5s: interrupted with Esc? (Esc fires no hook)",
		Queue:   []string{"q11 /compact keep: everything abo… running", "q12 /mcp reconnect some-long-serv… pending", "q13 /context pending"},
		Recent:  []string{"Notification(permission_prompt) 59m ago", "PermissionRequest(Bash) 59m ago", "PreToolUse(Bash) 59m ago", "UserPromptSubmit 59m ago", "SessionStart(compact) 59m ago"}}
	out := r.brief(now)
	if len(out) > 1200 {
		t.Fatalf("status brief is %d bytes:\n%s", len(out), out)
	}
	for _, want := range []string{"awaiting_permission", "ctx 99%", "permission_dialog", "[1. ", "note:", "recent:"} {
		if !strings.Contains(out, want) {
			t.Errorf("brief lacks %q:\n%s", want, out)
		}
	}
}

func TestReconcile(t *testing.T) {
	yes := func() bool { return true }
	no := func() bool { return false }
	cases := []struct {
		name      string
		hooks     journal.Activity
		r         report
		stillIdle func() bool
		want      string
		note      bool
	}{
		{"dialog wins", journal.Working, report{Dialog: "permission_dialog", Box: "none"}, yes, "awaiting_permission", false},
		{"unknown dialog", journal.Idle, report{Dialog: "unrecognized", Box: "none"}, yes, "dialog", false},
		{"esc-denied permission", journal.AwaitingPermission, report{Box: "empty"}, yes, "idle", true},
		{"approved, tool running", journal.AwaitingPermission, report{Box: "empty", Busy: true}, yes, "working", true},
		{"esc interrupt", journal.Working, report{Box: "empty"}, yes, "idle", true},
		{"brief idle flicker", journal.Working, report{Box: "empty"}, no, "working", false},
		{"blocked stop", journal.Stopped, report{Box: "empty", Busy: true}, yes, "working", true},
		{"agrees", journal.Stopped, report{Box: "empty"}, yes, "stopped", false},
		{"no hooks", journal.Unknown, report{Box: "empty"}, yes, "idle", true},
	}
	for _, c := range cases {
		r := c.r
		r.reconcile(c.hooks, c.stillIdle)
		if r.Activity != c.want || (r.Note != "") != c.note {
			t.Errorf("%s: Activity %q note %q; want %q note=%v", c.name, r.Activity, r.Note, c.want, c.note)
		}
	}
}

func TestDrift(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// A fresh install has no captures; the version gantline shipped
	// checked against still raises no warning.
	if d := drift("2.1.332"); d != "" {
		t.Errorf("drift on the shipped checked version: %s", d)
	}
	if d := drift("2.2.0"); !strings.Contains(d, "2.2.0") || !strings.Contains(d, "2.1.332") {
		t.Errorf("drift = %q", d)
	}
	os.MkdirAll(filepath.Join(audit.StateDir(), "corpus", "2.2.0"), 0o700)
	if d := drift("2.2.0"); d != "" {
		t.Errorf("drift on a version with local captures: %s", d)
	}
}
