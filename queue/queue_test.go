package queue

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/relaunch"
	"github.com/justinstimatze/gantline/screen"
	"github.com/justinstimatze/gantline/tmux"
)

// TestMain lets this test binary stand in for gantline's `launch`
// subcommand, which a relaunch's respawn-pane runs.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "launch" {
		fmt.Fprintln(os.Stderr, relaunch.RunLaunch(os.Args[2]))
		os.Exit(1)
	}
	// A test that forgets its own XDG_STATE_HOME still never writes to the
	// real state directory.
	dir, err := os.MkdirTemp("", "gantline-queue-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("XDG_STATE_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestAddIDsUnderConcurrency(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := Add("k", "/context", "", time.Minute, false, time.Now()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	items, err := Items("k")
	if err != nil || len(items) != 8 {
		t.Fatalf("Items = %d, %v", len(items), err)
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.ID] {
			t.Fatalf("duplicate id %s", it.ID)
		}
		seen[it.ID] = true
	}
}

func TestFoldAndFooter(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a, _ := Add("k", "/compact keep: step 2", "continue", time.Minute, false, time.Now())
	b, _ := Add("k", "/context", "", time.Minute, false, time.Now())
	Set("k", a.ID, Running, "")
	Set("k", a.ID, Done, "compacted")
	SetThen("k", a.ID, Done)
	items, _ := Items("k")
	if next, ok := Next(items); !ok || next.ID != b.ID {
		t.Fatalf("Next = %+v", next)
	}
	lines, ids := Footer(items, 5)
	if len(lines) != 1 || ids[0] != a.ID || !strings.Contains(lines[0], "done (compacted); follow-up done") {
		t.Fatalf("Footer = %q", lines)
	}
	MarkReported("k", a.ID)
	items, _ = Items("k")
	if lines, _ := Footer(items, 5); len(lines) != 0 {
		t.Fatalf("reported item came back: %q", lines)
	}
}

func TestGuardAndKind(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, u := range []bool{false, true} {
		for _, c := range []string{"/mcp", " /model "} {
			if !errors.Is(Guard(c, u), ErrPicker) {
				t.Errorf("Guard(%q, %v) let a picker through", c, u)
			}
		}
		for _, c := range []string{"/exit", "/quit", "/logout", "/login", "/background now", "/config model=haiku"} {
			if !errors.Is(Guard(c, u), ErrNever) {
				t.Errorf("Guard(%q, %v) = %v, want ErrNever", c, u, Guard(c, u))
			}
		}
		if !errors.Is(Guard("/model sonnet", u), ErrModelDefault) {
			t.Errorf("Guard(/model sonnet, %v) let it through", u)
		}
		for _, c := range []string{"/mcp reconnect x", "/compact", "/compact keep: the plan", "/clear", "/context",
			"/color cyan", "/color default", "hello", "  carry on  "} {
			if err := Guard(c, u); err != nil {
				t.Errorf("Guard(%q, %v) refused: %v", c, u, err)
			}
		}
	}
	// Commands and forms no real session has run through q: refused, and
	// let through with unchecked. Most open a dialog the runner can't close.
	for _, c := range []string{"/config", "/resume", "/permissions", "/export", "/color", "/color mauve",
		"/clear old-work", "/context all", "/mcp enable x", "/mcp reconnect", "/reload-skills", "/rename x", "/loop 5m /foo"} {
		if !errors.Is(Guard(c, false), ErrUnchecked) {
			t.Errorf("Guard(%q) = %v, want ErrUnchecked", c, Guard(c, false))
		}
		if err := Guard(c, true); err != nil {
			t.Errorf("Guard(%q, unchecked) refused: %v", c, err)
		}
	}
	if _, err := Add("k", "/mcp", "", time.Minute, false, time.Now()); !errors.Is(err, ErrPicker) {
		t.Errorf("Add queued a picker command: %v", err)
	}
	if _, err := Add("k", "/resume", "", time.Minute, false, time.Now()); !errors.Is(err, ErrUnchecked) {
		t.Errorf("Add queued an unchecked command: %v", err)
	}
	if _, err := AddUnchecked("k", "/resume", "", time.Minute, false, time.Now(), true); err != nil {
		t.Errorf("AddUnchecked refused: %v", err)
	}
	kinds := map[string]string{"/compact keep: x": "compact", "/model sonnet": "model", "/clear": "clear",
		"/mcp reconnect echomcp": "mcp-reconnect", "/context": "slash", "continue the task": "prompt"}
	for c, want := range kinds {
		if got := Kind(c); got != want {
			t.Errorf("Kind(%q) = %s, want %s", c, got, want)
		}
	}
}

// fakePane runs say/testdata/fake-claude.sh with env in a throwaway tmux
// server.
func fakePane(t *testing.T, env ...string) (tmux.Server, string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	script, _ := filepath.Abs("../say/testdata/fake-claude.sh")
	s := tmux.Server{Socket: fmt.Sprintf("gantline-q-%d-%d", os.Getpid(), time.Now().UnixNano())}
	cmd := strings.Join(append(env, "bash", script), " ")
	if _, err := s.Run("new-session", "-d", "-x", "200", "-y", "30", cmd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Run("kill-server") })
	for range 50 {
		if c, _ := s.Capture("%0"); strings.Contains(c, "❯") {
			return s, "%0"
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fake claude never drew its box")
	return s, ""
}

var fastRun = RunOptions{
	Verify: VerifyOptions{Start: 2 * time.Second, Finish: 3 * time.Second, Command: time.Second, Poll: 20 * time.Millisecond},
	Turn:   journal.WaitOptions{Quiet: 100 * time.Millisecond, Poll: 20 * time.Millisecond},
	Poll:   50 * time.Millisecond,
}

func eventsPath(key string) string { return filepath.Join(journal.PaneDir(key), "events.jsonl") }

func screenOf(s tmux.Server) string { c, _ := s.Capture("%0"); return c }

// The headline: /compact typed once the turn ends, confirmed from
// PreCompact/PostCompact, then the follow-up typed.
func TestRunCompactThenFollowUp(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	journal.Append("k", journal.Event{T: time.Now().Add(-time.Minute).UnixNano(), Ev: "SessionStart", Transcript: tr})
	s, pane := fakePane(t, "FAKE_EVENTS="+eventsPath("k"), "FAKE_TRANSCRIPT="+tr)
	it, _ := Add("k", "/compact keep: step 2", "continue the task", 10*time.Second, true, time.Now())
	b, _ := Add("k", "/clear", "", 10*time.Second, true, time.Now())
	if err := Run(s, pane, "k", fastRun); err != nil {
		t.Fatal(err)
	}
	items, _ := Items("k")
	if got := items[0]; got.ID != it.ID || got.State != Done || got.ThenState != Done {
		t.Fatalf("compact item = %+v\n%s", got, screenOf(s))
	}
	if got := items[1]; got.ID != b.ID || got.State != Done {
		t.Fatalf("clear item = %+v", got)
	}
	// Order on the journal: compaction, then the follow-up's turn, then /clear.
	evs, _ := journal.Events("k")
	var order []string
	for _, e := range evs {
		order = append(order, e.Ev)
	}
	want := "SessionStart PreCompact SessionStart PostCompact UserPromptSubmit Stop SessionStart"
	if strings.Join(order, " ") != want {
		t.Fatalf("events %v, want %s", order, want)
	}
}

// Queued mid-turn: nothing is typed until a Stop after the enqueue and an
// idle screen.
func TestRunWaitsForTurnEnd(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	s, pane := fakePane(t, "FAKE_BUSY_SECS=2", "FAKE_EVENTS="+eventsPath("k"))
	start := time.Now()
	Add("k", "/context", "", 10*time.Second, false, start)
	go func() {
		time.Sleep(700 * time.Millisecond)
		journal.Append("k", journal.Event{T: time.Now().UnixNano(), Ev: "Stop"})
	}()
	if err := Run(s, pane, "k", fastRun); err != nil {
		t.Fatal(err)
	}
	items, _ := Items("k")
	if items[0].State != Unconfirmed { // a fake /context writes no transcript
		t.Fatalf("item = %+v", items[0])
	}
	if typed := time.Unix(0, items[0].Updated); typed.Sub(start) < 1500*time.Millisecond {
		t.Fatalf("typed %v after queueing, before the busy screen cleared", typed.Sub(start))
	}
}

// A person's draft is never cleared: the item waits, then expires.
func TestRunNeverClearsDraft(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, pane := fakePane(t, "FAKE_DRAFT=half-typed")
	Add("k", "/context", "", 1500*time.Millisecond, true, time.Now())
	o := fastRun
	var mu sync.Mutex
	var logs []string
	o.Log = func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }
	Run(s, pane, "k", o)
	items, _ := Items("k")
	if items[0].State != Expired {
		t.Fatalf("item = %+v", items[0])
	}
	// The wait is said once, without the draft's text.
	mu.Lock()
	all := strings.Join(logs, "\n")
	mu.Unlock()
	if strings.Count(all, "waiting until") != 1 || !strings.Contains(all, "draft") || strings.Contains(all, "half-typed") {
		t.Fatalf("runner log:\n%s", all)
	}
	if c := screenOf(s); !strings.Contains(c, "half-typed") || strings.Contains(c, "SUBMITTED") {
		t.Fatalf("draft touched:\n%s", c)
	}
}

// Only one runner works a pane's queue at a time.
func TestRunSingleRunner(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	release, err := journal.Lock("k", "runner.lock", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	Add("k", "/context", "", time.Second, true, time.Now())
	if err := Run(tmux.Server{Socket: "unused"}, "%0", "k", fastRun); err != nil {
		t.Fatal(err)
	}
	items, _ := Items("k")
	if items[0].State != Pending {
		t.Fatalf("a second runner took the item: %+v", items[0])
	}
}

// The screen is idle throughout; only the journal says the turn is still
// running. Nothing is typed before its Stop.
func TestRunWaitsForStopEvenWhenScreenIdle(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	// With events, verification ends as soon as the prompt is typed, so
	// Updated tracks the typing.
	s, pane := fakePane(t, "FAKE_EVENTS="+eventsPath("k"))
	start := time.Now()
	Add("k", "hello", "", 5*time.Second, false, start)
	go func() {
		time.Sleep(800 * time.Millisecond)
		journal.Append("k", journal.Event{T: time.Now().UnixNano(), Ev: "Stop"})
	}()
	Run(s, pane, "k", fastRun)
	items, _ := Items("k")
	if typed := time.Unix(0, items[0].Updated); typed.Sub(start) < 800*time.Millisecond {
		t.Fatalf("typed %v after queueing, before the Stop", typed.Sub(start))
	}
	if !strings.Contains(screenOf(s), "SUBMITTED: hello") {
		t.Fatalf("never typed:\n%s", screenOf(s))
	}
}

// A follow-up goes in only after its command is confirmed done.
func TestRunNoFollowUpUnlessDone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, pane := fakePane(t) // no FAKE_EVENTS: the compaction is never confirmed
	Add("k", "/compact", "continue the task", 5*time.Second, true, time.Now())
	Run(s, pane, "k", fastRun)
	items, _ := Items("k")
	if items[0].State == Done || items[0].ThenState != "" {
		t.Fatalf("item = %+v", items[0])
	}
	if strings.Contains(screenOf(s), "continue the task") {
		t.Fatalf("follow-up typed after an unconfirmed compaction:\n%s", screenOf(s))
	}
}

// /model opens a confirmation. The runner never answers it: it records
// that a person is needed, waits, and verifies once someone confirms.
// Guard refuses /model <name> now; the item is written directly, standing
// in for any command that opens a dialog.
func TestRunWaitsForPersonOnConfirm(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	s, pane := fakePane(t, "FAKE_CONFIRM=1", "FAKE_EVENTS="+eventsPath("k"))
	write("k", record{Op: "add", Item: Item{ID: "q1", Cmd: "/model sonnet", Enq: time.Now().UnixNano(),
		Timeout: int64(10 * time.Second), IdleAtEnq: true, State: Pending}})
	done := make(chan struct{})
	go func() { Run(s, pane, "k", fastRun); close(done) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		items, _ := Items("k")
		if strings.Contains(items[0].Detail, "waiting for a person to answer: Switch model?") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner never reported the dialog: %+v\n%s", items[0], screenOf(s))
		}
	}
	time.Sleep(300 * time.Millisecond)
	if c := screenOf(s); !strings.Contains(c, "Switch model?") {
		t.Fatalf("the runner answered the dialog itself:\n%s", c)
	}
	s.Run("send-keys", "-t", pane, "Enter") // the person confirms
	<-done
	items, _ := Items("k")
	if items[0].State != Done || !strings.Contains(items[0].Detail, "claude-sonnet") {
		t.Fatalf("item = %+v", items[0])
	}
}

// Claude Code's dimmed prompt suggestion in an empty box is not a draft:
// the item is typed, not left waiting.
func TestRunTypesOverSuggestion(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, pane := fakePane(t, "FAKE_GHOST='read steps/3.txt and do what it says'")
	Add("k", "/context", "", 4*time.Second, true, time.Now())
	Run(s, pane, "k", fastRun)
	items, _ := Items("k")
	if items[0].State != Unconfirmed {
		t.Fatalf("item = %+v\n%s", items[0], screenOf(s))
	}
}

// Queued a moment after the Stop of the turn it was queued in: no further
// turn end is coming, so the runner must not wait for one.
func TestRunQueuedJustAfterStop(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, pane := fakePane(t)
	now := time.Now()
	journal.Append("k", journal.Event{T: now.Add(-2 * time.Second).UnixNano(), Ev: "UserPromptSubmit"})
	journal.Append("k", journal.Event{T: now.Add(-5 * time.Millisecond).UnixNano(), Ev: "Stop"})
	Add("k", "/context", "", 3*time.Second, false, now)
	Run(s, pane, "k", fastRun)
	items, _ := Items("k")
	if items[0].State != Unconfirmed {
		t.Fatalf("item = %+v", items[0])
	}
}

// An open compaction holds the item even when the screen reads idle: keys
// typed during one never reach the box.
func TestRunWaitsOutCompaction(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, pane := fakePane(t)
	now := time.Now()
	journal.Append("k", journal.Event{T: now.Add(-time.Second).UnixNano(), Ev: "PreCompact", Trigger: "auto"})
	Add("k", "/context", "", 1500*time.Millisecond, true, now)
	Run(s, pane, "k", fastRun)
	items, _ := Items("k")
	if items[0].State != Expired {
		t.Fatalf("typed during a compaction: %+v", items[0])
	}
}

// relaunchPane starts the fake with env and returns the options whose
// FindSession describes it as a claude launched with --model haiku.
func relaunchPane(t *testing.T, env ...string) (tmux.Server, string, RunOptions) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"model":"opus"}`), 0o600)
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	env = append(env, "FAKE_EVENTS="+eventsPath("k"), "FAKE_STATUS="+filepath.Join(journal.PaneDir("k"), "status.json"))
	s, pane := fakePane(t, env...)
	script, _ := filepath.Abs("../say/testdata/fake-claude.sh")
	o := fastRun
	o.FindSession = func(pid int) (relaunch.Session, error) {
		return relaunch.Session{PID: pid, Argv: []string{script, "--model", "haiku"}, Dir: home,
			Env: append([]string{"PATH=" + os.Getenv("PATH")}, env...), ID: "00000000-0000-4000-8000-000000000001"}, nil
	}
	return s, pane, o
}

func TestRunRelaunchesWithModel(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, pane, o := relaunchPane(t)
	AddModel("k", "sonnet", "carry on", 10*time.Second, true, time.Now())
	if err := Run(s, pane, "k", o); err != nil {
		t.Fatal(err)
	}
	items, _ := Items("k")
	if it := items[0]; it.State != Done || !strings.Contains(it.Detail, "claude-sonnet-5-5") || it.ThenState != Done {
		t.Fatalf("item = %+v\n%s", it, screenOf(s))
	}
	if strings.Contains(items[0].Detail, "settings") || settingsModel() != "opus" {
		t.Fatalf("settings default touched: %q (%s)", settingsModel(), items[0].Detail)
	}
	if c := screenOf(s); !strings.Contains(c, "SUBMITTED: carry on") {
		t.Fatalf("follow-up not typed into the relaunched session:\n%s", c)
	}
}

func TestRunRelaunchRejectsWrongModel(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, pane, o := relaunchPane(t, "FAKE_FORCE_MODEL=haiku")
	AddModel("k", "sonnet", "carry on", 10*time.Second, true, time.Now())
	Run(s, pane, "k", o)
	items, _ := Items("k")
	if it := items[0]; it.State != Rejected || it.ThenState != "" {
		t.Fatalf("item = %+v", it)
	}
}

// A person's draft in the box is never thrown away by a relaunch.
func TestRunRelaunchWaitsOutDraft(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, pane, o := relaunchPane(t, "FAKE_DRAFT=half-typed")
	AddModel("k", "sonnet", "", 1500*time.Millisecond, true, time.Now())
	Run(s, pane, "k", o)
	items, _ := Items("k")
	if items[0].State != Expired || !strings.Contains(screenOf(s), "half-typed") {
		t.Fatalf("item = %+v\n%s", items[0], screenOf(s))
	}
}

func TestGuardModel(t *testing.T) {
	if err := Guard("/model sonnet", false); !errors.Is(err, ErrModelDefault) {
		t.Fatalf("Guard(/model sonnet) = %v", err)
	}
	if _, err := AddModel("k", "--print", "", time.Second, true, time.Now()); err == nil {
		t.Fatal("a flag accepted as a model name")
	}
}

// Keys typed while a session is still starting are dropped, not queued:
// the runner types again once the box shows nothing of them.
func TestRunRetypesDroppedKeys(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	s, pane := fakePane(t, "FAKE_DEAF_SECS=2")
	Add("k", "/context", "", 30*time.Second, true, time.Now())
	o := fastRun
	o.DropWait = 700 * time.Millisecond
	Run(s, pane, "k", o)
	items, _ := Items("k")
	if items[0].State == Failed || !strings.Contains(screenOf(s), "SUBMITTED: /context") {
		t.Fatalf("item = %+v\n%s", items[0], screenOf(s))
	}
}

// Keys that never showed in the box but that Claude Code queued (a
// queue-operation enqueue in the transcript) are not typed again.
func TestRunNeverRetypesQueuedKeys(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	// The enqueue must land after typing begins, and the item must outlast
	// FAKE_LATE_SECS plus the late-keys wait: margins sized for a loaded
	// CI runner.
	at := time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339Nano)
	os.WriteFile(tr, []byte(`{"type":"queue-operation","operation":"enqueue","timestamp":"`+at+`","content":"/context"}`+"\n"), 0o600)
	journal.Append("k", journal.Event{T: time.Now().Add(-time.Minute).UnixNano(), Ev: "SessionStart", Transcript: tr})
	s, pane := fakePane(t, "FAKE_DEAF_SECS=3")
	Add("k", "/context", "", 30*time.Second, true, time.Now())
	o := fastRun
	o.DropWait = 700 * time.Millisecond
	Run(s, pane, "k", o)
	items, _ := Items("k")
	if items[0].State != Unconfirmed || strings.Contains(screenOf(s), "SUBMITTED") {
		t.Fatalf("item = %+v\n%s", items[0], screenOf(s))
	}
}

// A /compact that Claude Code queued runs once the turn ends. The runner
// never retypes it, and confirms it from the late PreCompact and
// PostCompact; an automatic compaction in the meantime does not count.
func TestRunConfirmsCompactFromClaudesQueue(t *testing.T) {
	for _, trig := range []string{"manual", "auto"} {
		t.Run(trig, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			os.MkdirAll(journal.PaneDir("k"), 0o700)
			tr := filepath.Join(t.TempDir(), "t.jsonl")
			// The enqueue must land after typing begins, and the item must outlast
			// FAKE_LATE_SECS plus the late-keys wait: margins sized for a loaded
			// CI runner.
			at := time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339Nano)
			os.WriteFile(tr, []byte(`{"type":"queue-operation","operation":"enqueue","timestamp":"`+at+`","content":"/compact keep x"}`+"\n"), 0o600)
			journal.Append("k", journal.Event{T: time.Now().Add(-time.Minute).UnixNano(), Ev: "SessionStart", Transcript: tr})
			s, pane := fakePane(t, "FAKE_DEAF_SECS=3")
			go func() {
				time.Sleep(3 * time.Second)
				journal.Append("k", journal.Event{T: time.Now().UnixNano(), Ev: "PreCompact", Trigger: trig})
				journal.Append("k", journal.Event{T: time.Now().UnixNano(), Ev: "PostCompact", Trigger: trig})
				f, _ := os.OpenFile(tr, os.O_APPEND|os.O_WRONLY, 0)
				fmt.Fprintf(f, `{"type":"system","subtype":"compact_boundary","timestamp":%q,"compactMetadata":{"trigger":%q}}`+"\n", time.Now().UTC().Format(time.RFC3339Nano), trig)
				f.Close()
			}()
			Add("k", "/compact keep x", "", 6*time.Second, true, time.Now())
			o := fastRun
			o.DropWait = 700 * time.Millisecond
			Run(s, pane, "k", o)
			items, _ := Items("k")
			want := Done
			if trig == "auto" {
				want = Unconfirmed
			}
			if items[0].State != want || strings.Contains(screenOf(s), "SUBMITTED") {
				t.Fatalf("item = %+v, want %s\n%s", items[0], want, screenOf(s))
			}
		})
	}
}

// An item whose runner died stays "running" with nobody to finish it. The
// next runner reports it unconfirmed and never types it again.
func TestRunSettlesOrphanedItem(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	s, pane := fakePane(t)
	Add("k", "/context", "carry on", 10*time.Second, true, time.Now())
	items, _ := Items("k")
	Set("k", items[0].ID, Running, "")
	Run(s, pane, "k", fastRun)
	items, _ = Items("k")
	if items[0].State != Unconfirmed || strings.Contains(screenOf(s), "SUBMITTED") {
		t.Fatalf("item = %+v\n%s", items[0], screenOf(s))
	}
}

// Keys that showed in the box only after the turn ended sit there unsent:
// the runner presses Enter on them, since the box holds exactly its text.
func TestRunSubmitsItsKeysThatReachTheBoxLate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	// The enqueue must land after typing begins, and the item must outlast
	// FAKE_LATE_SECS plus the late-keys wait: margins sized for a loaded
	// CI runner.
	at := time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339Nano)
	os.WriteFile(tr, []byte(`{"type":"queue-operation","operation":"enqueue","timestamp":"`+at+`","content":"/context"}`+"\n"), 0o600)
	journal.Append("k", journal.Event{T: time.Now().Add(-time.Minute).UnixNano(), Ev: "SessionStart", Transcript: tr})
	s, pane := fakePane(t, "FAKE_LATE_SECS=3")
	Add("k", "/context", "", 30*time.Second, true, time.Now())
	o := fastRun
	o.DropWait = 700 * time.Millisecond
	Run(s, pane, "k", o)
	if !strings.Contains(screenOf(s), "SUBMITTED: /context") {
		items, _ := Items("k")
		t.Fatalf("late keys left in the box: item = %+v\n%s", items[0], screenOf(s))
	}
}

func TestOutput(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	typed := time.Now()
	line := func(at time.Time, out string) string {
		return `{"type":"system","subtype":"local_command","timestamp":"` + at.UTC().Format(time.RFC3339Nano) +
			`","content":"<local-command-stdout>` + out + `</local-command-stdout>"}` + "\n"
	}
	os.WriteFile(tr, []byte(line(typed.Add(-time.Minute), "older")+line(typed.Add(time.Second), "Context usage 76.7k/300k tokens (26%)")), 0o600)
	journal.Append("k", journal.Event{T: typed.Add(-time.Hour).UnixNano(), Ev: "SessionStart", Transcript: tr})
	it, _ := Add("k", "/context", "", time.Minute, true, typed)
	if _, err := Output("k", it.ID); err == nil {
		t.Fatal("output for an item never typed")
	}
	SetTyped("k", it.ID, typed)
	if out, err := Output("k", it.ID); err != nil || out != "Context usage 76.7k/300k tokens (26%)" {
		t.Fatalf("Output = %q, %v", out, err)
	}
}

// Text the runner typed but couldn't submit is its own, not a person's
// draft: the runner clears it and types again instead of waiting forever.
// A different draft is still left alone.
func TestRunClearsItsOwnLeftoverText(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	s, pane := fakePane(t)
	s.Run("send-keys", "-t", pane, "-l", "/context")
	Add("k", "/context", "", 3*time.Second, true, time.Now())
	Run(s, pane, "k", fastRun)
	if items, _ := Items("k"); !strings.Contains(screenOf(s), "SUBMITTED") || items[0].State == Expired {
		t.Fatalf("item = %+v\n%s", items[0], screenOf(s))
	}

	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	s, pane = fakePane(t)
	s.Run("send-keys", "-t", pane, "-l", "a person's draft")
	Add("k", "/context", "", 2*time.Second, true, time.Now())
	Run(s, pane, "k", fastRun)
	if items, _ := Items("k"); items[0].State != Expired || !strings.Contains(screenOf(s), "a person's draft") {
		t.Fatalf("draft touched: item = %+v\n%s", items[0], screenOf(s))
	}
}

// An expired item says which idle check kept failing and keeps the bottom
// of the screen it gave up on.
func TestRunExpiryShowsWhy(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	s, pane := fakePane(t, "FAKE_BUSY=1")
	Add("k", "/context", "", 1500*time.Millisecond, true, time.Now())
	Run(s, pane, "k", fastRun)
	items, _ := Items("k")
	if it := items[0]; it.State != Expired || !strings.Contains(it.Detail, "turn running") || !strings.Contains(it.Screen, "esc to interrupt") {
		t.Fatalf("item = %+v", it)
	}
}

// A finished slash command's screen block is kept with the item, taken
// before the follow-up is typed, for `q --show <id> --screen`.
func TestRunKeepsScreenCopy(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	at := time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339Nano)
	os.WriteFile(tr, []byte(`{"type":"system","subtype":"local_command","timestamp":"`+at+`","content":"<local-command-stdout>Context usage 1k/300k tokens (0%)</local-command-stdout>"}`+"\n"), 0o600)
	journal.Append("k", journal.Event{T: time.Now().Add(-time.Minute).UnixNano(), Ev: "SessionStart", Transcript: tr})
	s, pane := fakePane(t)
	it, _ := Add("k", "/context", "", 10*time.Second, true, time.Now())
	Run(s, pane, "k", fastRun)
	items, _ := Items("k")
	got, err := ScreenOutput("k", it.ID)
	if items[0].State != Done || err != nil || !strings.Contains(got, "/context") {
		t.Fatalf("item = %+v; screen copy %q, %v\n%s", items[0], got, err, screenOf(s))
	}
}

// A long /compact in a short pane scrolls the box: the runner still types
// it, confirms it and reports done, rather than waiting out its timeout
// for an input box it can't see.
func TestRunCompactsInShortPane(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	journal.Append("k", journal.Event{T: time.Now().Add(-time.Minute).UnixNano(), Ev: "SessionStart", Transcript: tr})
	s, pane := fakePane(t, "FAKE_BOX_ROWS=3", "FAKE_EVENTS="+eventsPath("k"), "FAKE_TRANSCRIPT="+tr)
	cmd := "/compact Keep: " + strings.Repeat("the loop id, the branch and the next steps; ", 8) + "END"
	Add("k", cmd, "", 10*time.Second, true, time.Now())
	if err := Run(s, pane, "k", fastRun); err != nil {
		t.Fatal(err)
	}
	if items, _ := Items("k"); items[0].State != Done {
		t.Fatalf("item = %+v\n%s", items[0], screenOf(s))
	}
}

// Whatever stops a delivery partway, the runner's own text doesn't stay in
// the box, where the next thing typed would join it.
func TestRunLeavesNoUnsentText(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	s, pane := fakePane(t, "FAKE_BOX_ROWS=3", "FAKE_SWALLOW=99")
	cmd := "/compact Keep: " + strings.Repeat("the loop id, the branch and the next steps; ", 8) + "END"
	Add("k", cmd, "", 5*time.Second, true, time.Now())
	Run(s, pane, "k", fastRun)
	items, _ := Items("k")
	if items[0].State == Done {
		t.Fatalf("item = %+v; every Enter was swallowed", items[0])
	}
	if in, _ := screen.Input(screenOf(s)); in != "" {
		t.Fatalf("box still holds %q after %s: %s\n%s", in, items[0].State, items[0].Detail, screenOf(s))
	}
}
