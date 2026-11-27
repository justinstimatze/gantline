package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/queue"
)

// buildGantline builds this package's binary once per test, so hook and
// statusline run as Claude Code runs them: stdin in, stdout and an exit
// code out.
func buildGantline(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gantline")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// runBin runs bin with stdin and the given pane env ("" for outside tmux),
// returning stdout and the exit code.
func runBin(t *testing.T, bin, state, pane, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	env := []string{"XDG_STATE_HOME=" + state, "PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if pane != "" {
		env = append(env, "TMUX=/tmp/gl-test-sock,1,0", "TMUX_PANE="+pane)
	}
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), 0
}

const statusPayload = `{"session_id":"s1","version":"2.1.332","model":{"id":"claude-haiku-x","display_name":"Haiku"},"context_window":{"used_percentage":42.5}}`

// The README promises the wrapped statusline still prints what it printed
// before: same stdin, same stdout, same exit code, whether or not gantline
// can read the payload or is inside tmux.
func TestStatuslineWrapperIsTransparent(t *testing.T) {
	bin := buildGantline(t)
	state := t.TempDir()
	key := journal.Key("/tmp/gl-test-sock", "%7")
	wrapped := []string{"statusline", "--", "sh", "-c", `cat; printf ' [%s]' "$1"`, "sh", "arg one"}

	for _, tc := range []struct{ name, pane, in string }{
		{"in tmux", "%7", statusPayload},
		{"outside tmux", "", statusPayload},
		{"unreadable payload", "%7", "not json"},
	} {
		out, code := runBin(t, bin, state, tc.pane, tc.in, wrapped...)
		if want := tc.in + " [arg one]"; out != want || code != 0 {
			t.Errorf("%s: stdout %q exit %d, want %q exit 0", tc.name, out, code, want)
		}
	}
	t.Setenv("XDG_STATE_HOME", state)
	st, err := journal.ReadStatus(key)
	if err != nil || st.ModelID != "claude-haiku-x" || !st.HasContext || st.ContextPct != 42.5 || st.Version != "2.1.332" {
		t.Errorf("status.json = %+v, %v; want the in-tmux payload kept", st, err)
	}

	if _, code := runBin(t, bin, state, "%7", statusPayload, "statusline", "--", "sh", "-c", "exit 3"); code != 3 {
		t.Errorf("wrapped exit 3 came back as %d", code)
	}
	if out, code := runBin(t, bin, state, "%7", statusPayload, "statusline"); out != "" || code != 0 {
		t.Errorf("bare statusline: stdout %q exit %d, want nothing and 0", out, code)
	}
}

// The hook never blocks a session: it exits 0 and prints nothing on any
// input, except queue news on UserPromptSubmit, which it prints once.
func TestHookJournalsAndReportsOnce(t *testing.T) {
	bin := buildGantline(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	key := journal.Key("/tmp/gl-test-sock", "%7")

	for _, tc := range []struct{ name, pane, in string }{
		{"stop", "%7", `{"hook_event_name":"Stop","session_id":"s1"}`},
		{"garbage", "%7", "{{{"},
		{"empty", "%7", ""},
		{"outside tmux", "", `{"hook_event_name":"Stop","session_id":"s1"}`},
	} {
		if out, code := runBin(t, bin, state, tc.pane, tc.in, "hook", "Stop"); out != "" || code != 0 {
			t.Errorf("%s: stdout %q exit %d, want nothing and 0", tc.name, out, code)
		}
	}
	evs, err := journal.Events(key)
	if err != nil {
		t.Fatal(err)
	}
	stops := 0
	for _, e := range evs {
		if e.Ev == "Stop" {
			stops++
		}
	}
	if stops < 1 {
		t.Errorf("journal has %d Stop events, want the in-tmux Stop recorded: %+v", stops, evs)
	}
	if dirs, _ := os.ReadDir(journal.Dir()); len(dirs) != 1 {
		t.Errorf("pane dirs %v, want only %s (the outside-tmux call writes nothing)", dirs, key)
	}

	it, err := queue.Add(key, "/context", "", time.Minute, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Set(key, it.ID, queue.Failed, "test outcome"); err != nil {
		t.Fatal(err)
	}
	prompt := `{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"hi"}`
	out, code := runBin(t, bin, state, "%7", prompt, "hook", "UserPromptSubmit")
	if code != 0 || !strings.Contains(out, it.ID) || !strings.Contains(out, "/context") {
		t.Errorf("first prompt: stdout %q exit %d, want a line about %s", out, code, it.ID)
	}
	if out, _ := runBin(t, bin, state, "%7", prompt, "hook", "UserPromptSubmit"); out != "" {
		t.Errorf("second prompt reported again: %q", out)
	}
}
