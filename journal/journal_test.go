package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ev builds an event at base+ms.
var base = time.Unix(1_800_000_000, 0)

func ev(ms int, name string, f ...func(*Event)) Event {
	e := Event{T: base.Add(time.Duration(ms) * time.Millisecond).UnixNano(), Ev: name}
	for _, fn := range f {
		fn(&e)
	}
	return e
}

func ntype(t string) func(*Event)   { return func(e *Event) { e.NotificationType = t } }
func source(s string) func(*Event)  { return func(e *Event) { e.Source = s } }
func trigger(s string) func(*Event) { return func(e *Event) { e.Trigger = s } }
func toModel(m string) func(*Event) { return func(e *Event) { e.ToModel = m } }

// Sequences follow the real orders captured in the P0 probes.
func TestFold(t *testing.T) {
	cases := []struct {
		name string
		evs  []Event
		want Activity
	}{
		{"fresh session", []Event{ev(0, "SessionStart", source("startup"))}, Idle},
		{"normal turn", []Event{ev(0, "UserPromptSubmit"), ev(1, "PreToolUse"), ev(2, "PostToolUse"), ev(3, "Stop")}, Stopped},
		{"blocked stop continues", []Event{ev(0, "UserPromptSubmit"), ev(1, "Stop"), ev(2, "PreToolUse")}, Working},
		{"stray SubagentStop after Stop", []Event{ev(0, "UserPromptSubmit"), ev(1, "Stop"), ev(2, "SubagentStop")}, Stopped},
		{"permission dialog", []Event{ev(0, "UserPromptSubmit"), ev(1, "PreToolUse"), ev(2, "PermissionRequest")}, AwaitingPermission},
		{"permission notification", []Event{ev(0, "PermissionRequest"), ev(6000, "Notification", ntype("permission_prompt"))}, AwaitingPermission},
		{"permission granted, tool ran", []Event{ev(0, "PermissionRequest"), ev(1, "PostToolUse")}, Working},
		{"esc interrupt fires nothing", []Event{ev(0, "UserPromptSubmit")}, Working},
		{"idle prompt", []Event{ev(0, "Stop"), ev(60000, "Notification", ntype("idle_prompt"))}, Idle},
		{"compact in progress", []Event{ev(0, "Stop"), ev(1, "PreCompact", trigger("manual")), ev(2, "SessionStart", source("compact"))}, Compacting},
		{"manual compact done", []Event{ev(0, "Stop"), ev(1, "PreCompact", trigger("manual")), ev(2, "SubagentStop"), ev(3, "SessionStart", source("compact")), ev(4, "PostCompact", trigger("manual"))}, Idle},
		{"auto compact mid-turn", []Event{ev(0, "UserPromptSubmit"), ev(1, "PreCompact", trigger("auto")), ev(2, "PostCompact", trigger("auto"))}, Working},
		{"clear", []Event{ev(0, "Stop"), ev(1, "SessionStart", source("clear"))}, Idle},
		{"stop failure", []Event{ev(0, "UserPromptSubmit"), ev(1, "StopFailure")}, Stopped},
		{"session end", []Event{ev(0, "Stop"), ev(1, "SessionEnd")}, Ended},
		{"empty", nil, Unknown},
	}
	for _, c := range cases {
		if got := Fold(c.evs, Status{}).Activity; got != c.want {
			t.Errorf("%s: Activity = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestFoldFields(t *testing.T) {
	evs := []Event{
		ev(0, "SessionStart", source("startup"), func(e *Event) { e.Model = "claude-haiku-5-5"; e.Sid = "a" }),
		ev(1, "UserPromptSubmit", func(e *Event) { e.PermissionMode = "auto"; e.Transcript = "/t.jsonl" }),
		ev(2, "PostModelSwitch", toModel("claude-sonnet-5-5")),
		ev(3, "PreCompact", trigger("manual")),
		ev(4, "SessionStart", source("compact"), func(e *Event) { e.Sid = "b" }),
		ev(5, "PostCompact", trigger("manual")),
		ev(6, "StopFailure", func(e *Event) { e.Error = "rate_limit" }),
	}
	s := Fold(evs, Status{})
	if s.Model != "claude-sonnet-5-5" || s.PermissionMode != "auto" || s.SessionID != "b" ||
		s.Compactions != 1 || s.LastError != "rate_limit" || s.Transcript != "/t.jsonl" || s.Events != 7 {
		t.Fatalf("Fold = %+v", s)
	}
	// A statusline snapshot newer than the last switch wins for the model.
	newer := Status{T: base.Add(time.Second).UnixNano(), ModelID: "claude-opus-5-5", ContextPct: 12.5, HasContext: true, Version: "2.1.332"}
	s = Fold(evs, newer)
	if s.Model != "claude-opus-5-5" || s.ContextPct != 12.5 || s.Version != "2.1.332" {
		t.Fatalf("Fold with status = %+v", s)
	}
	older := Status{T: base.UnixNano(), ModelID: "claude-opus-5-5"}
	if s = Fold(evs, older); s.Model != "claude-sonnet-5-5" {
		t.Fatalf("an older snapshot overrode the switch: %s", s.Model)
	}
}

// TestFromHookKeepsNoContent guards the privacy rule: prompt text, assistant
// messages, tool inputs and summaries never reach the journal.
func TestFromHookKeepsNoContent(t *testing.T) {
	raw := `{"hook_event_name":"PostCompact","session_id":"s","trigger":"manual",
	"prompt":"SECRET-PROMPT","last_assistant_message":"SECRET-REPLY",
	"tool_input":{"command":"SECRET-CMD"},"tool_response":"SECRET-OUT",
	"compact_summary":"SECRET-SUMMARY","custom_instructions":"SECRET-KEEP"}`
	e, err := FromHook([]byte(raw), "", base)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "SECRET") {
		t.Fatalf("content leaked into the journal: %s", b)
	}
	if e.SummaryLen != len("SECRET-SUMMARY") || !e.HasInstructions || e.Trigger != "manual" {
		t.Fatalf("FromHook = %+v", e)
	}
}

func TestFromHookStopAlias(t *testing.T) {
	e, err := FromHook(nil, "stop", base)
	if err != nil || e.Ev != "Stop" {
		t.Fatalf("FromHook(nil, stop) = %+v, %v", e, err)
	}
	if _, err := FromHook([]byte(`{}`), "", base); err == nil {
		t.Fatal("no event name was accepted")
	}
}

func TestAppendEventsRotate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	k := Key("/tmp/tmux-1000/default", "%3")
	for i := range 3 {
		if err := Append(k, ev(i, "Stop")); err != nil {
			t.Fatal(err)
		}
	}
	// Force a rotation, then append once more: nothing is lost.
	path := filepath.Join(PaneDir(k), "events.jsonl")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.Write([]byte(strings.Repeat("x", MaxBytes) + "\n"))
	f.Close()
	Append(k, ev(9, "UserPromptSubmit"))
	evs, err := Events(k)
	if err != nil || len(evs) != 4 || evs[3].Ev != "UserPromptSubmit" {
		t.Fatalf("Events = %d events, %v", len(evs), err)
	}
	if _, err := os.Stat(filepath.Join(PaneDir(k), "events.1.jsonl")); err != nil {
		t.Fatal("no rotation happened")
	}
}

func TestStatusRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	raw := `{"session_id":"s","version":"2.1.332","model":{"id":"claude-haiku-5-5","display_name":"Haiku 5.5"},
	"context_window":{"used_percentage":8},"cost":{"total_cost_usd":1}}`
	st, err := StatusFromStatusline([]byte(raw), base)
	if err != nil {
		t.Fatal(err)
	}
	k := Key("/s", "%1")
	if err := WriteStatus(k, st); err != nil {
		t.Fatal(err)
	}
	got, err := ReadStatus(k)
	if err != nil || got.ModelID != "claude-haiku-5-5" || got.ContextPct != 8 || !got.HasContext || got.Version != "2.1.332" {
		t.Fatalf("ReadStatus = %+v, %v", got, err)
	}
}

func TestKeySeparatesServers(t *testing.T) {
	a, b := Key("/tmp/tmux-1000/default", "%0"), Key("/tmp/tmux-1000/gl-q", "%0")
	if a == b {
		t.Fatalf("same pane id on two servers got one key %q", a)
	}
	t.Setenv("TMUX", "/tmp/tmux-1000/gl-q,123,0")
	t.Setenv("TMUX_PANE", "%0")
	if KeyFromEnv() != b {
		t.Fatalf("KeyFromEnv = %q, want %q", KeyFromEnv(), b)
	}
}

var fast = WaitOptions{Timeout: 3 * time.Second, Quiet: 300 * time.Millisecond, Poll: 20 * time.Millisecond}

func now(name string) Event { return Event{T: time.Now().UnixNano(), Ev: name} }

func TestWaitTurnEndIgnoresEarlierStop(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	Append("k", Event{T: time.Now().Add(-time.Minute).UnixNano(), Ev: "Stop"})
	o := fast
	o.Timeout = 500 * time.Millisecond
	if err := WaitTurnEnd("k", time.Now(), o); err != ErrTimeout {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

// A blocked stop: Stop, then more of the turn. WaitTurnEnd must hold until
// the turn really stops and the transcript goes quiet.
func TestWaitTurnEndHoldsThroughBlockedStop(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	transcript := filepath.Join(dir, "t.jsonl")
	os.WriteFile(transcript, []byte("{}\n"), 0o600)
	start := time.Now()
	go func() {
		time.Sleep(100 * time.Millisecond)
		e := now("Stop")
		e.Transcript = transcript
		Append("k", e)
		time.Sleep(50 * time.Millisecond)
		Append("k", now("PreToolUse")) // another hook blocked the stop
		for range 5 {
			time.Sleep(150 * time.Millisecond)
			f, _ := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("{}\n")
			f.Close()
		}
		Append("k", now("Stop"))
	}()
	if err := WaitTurnEnd("k", start, fast); err != nil {
		t.Fatal(err)
	}
	// 100 + 50 + 5×150 ms of turn, then 300 ms quiet
	if took := time.Since(start); took < 1200*time.Millisecond {
		t.Fatalf("WaitTurnEnd returned after %v, before the blocked turn finished", took)
	}
}

func TestWaitFor(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	start := time.Now()
	go func() {
		time.Sleep(50 * time.Millisecond)
		Append("k", now("PreCompact"))
		e := now("PostModelSwitch")
		e.RequestedModel = "sonnet"
		Append("k", e)
	}()
	e, err := WaitFor("k", start, fast, func(e Event) bool { return e.Ev == "PostModelSwitch" && e.RequestedModel == "sonnet" })
	if err != nil || e.Ev != "PostModelSwitch" {
		t.Fatalf("WaitFor = %+v, %v", e, err)
	}
}

func TestLockWritesExcludes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	release, err := LockWrites("k", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockWrites("k", 100*time.Millisecond); err != ErrLocked {
		t.Fatalf("second lock: err = %v, want ErrLocked", err)
	}
	if r, err := LockWrites("other", 100*time.Millisecond); err != nil {
		t.Fatalf("another pane's lock was blocked: %v", err)
	} else {
		r()
	}
	release()
	r2, err := LockWrites("k", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("lock not free after release: %v", err)
	}
	r2()
}

func TestCompactionOpen(t *testing.T) {
	now := time.Now()
	at := func(ago time.Duration, ev string) Event { return Event{T: now.Add(-ago).UnixNano(), Ev: ev} }
	cases := []struct {
		name string
		evs  []Event
		want bool
	}{
		{"none", nil, false},
		{"open, tools running", []Event{at(time.Minute, "PreCompact"), at(30*time.Second, "PostToolUse")}, true},
		{"finished", []Event{at(time.Minute, "PreCompact"), at(10*time.Second, "PostCompact")}, false},
		{"second open", []Event{at(3*time.Minute, "PreCompact"), at(2*time.Minute, "PostCompact"), at(time.Minute, "PreCompact")}, true},
		{"stale", []Event{at(6*time.Minute, "PreCompact")}, false},
	}
	for _, c := range cases {
		if got := CompactionOpen(c.evs, now); got != c.want {
			t.Errorf("%s: CompactionOpen = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBusyAt(t *testing.T) {
	t0 := time.Now()
	at := func(s float64, ev string) Event {
		return Event{T: t0.Add(time.Duration(s * float64(time.Second))).UnixNano(), Ev: ev}
	}
	cases := []struct {
		name string
		evs  []Event
		when float64
		want int
	}{
		{"idle after stop", []Event{at(0, "UserPromptSubmit"), at(5, "Stop")}, 8, 0},
		{"mid turn", []Event{at(0, "UserPromptSubmit"), at(5, "Stop"), at(9, "UserPromptSubmit")}, 10, 1},
		{"blocked stop, tools after", []Event{at(0, "UserPromptSubmit"), at(5, "Stop"), at(9, "PostToolUse")}, 12, 1},
		{"tools long ago", []Event{at(0, "UserPromptSubmit"), at(5, "Stop"), at(9, "PostToolUse")}, 30, 0},
		{"compacting", []Event{at(0, "Stop"), at(2, "PreCompact")}, 5, 1},
		{"fresh session", []Event{at(0, "UserPromptSubmit"), at(3, "SessionStart")}, 5, 0},
		{"later events ignored", []Event{at(0, "Stop"), at(9, "UserPromptSubmit")}, 5, 0},
	}
	for _, c := range cases {
		if got := BusyAt(c.evs, t0.Add(time.Duration(c.when*float64(time.Second)))); len(got) != c.want {
			t.Errorf("%s: BusyAt = %q, want %d reasons", c.name, got, c.want)
		}
	}
}
