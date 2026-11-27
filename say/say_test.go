package say

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/tmux"
)

var fast = Options{Wait: 500 * time.Millisecond, Poll: 20 * time.Millisecond}

// fakePane runs testdata/fake-claude.sh with env in a throwaway tmux server.
func fakePane(t *testing.T, env ...string) (tmux.Server, string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	script, _ := filepath.Abs("testdata/fake-claude.sh")
	s := tmux.Server{Socket: fmt.Sprintf("gantline-say-%d-%d", os.Getpid(), time.Now().UnixNano())}
	cmd := strings.Join(append(env, "bash", script), " ")
	if _, err := s.Run("new-session", "-d", "-x", "200", "-y", "30", cmd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Run("kill-server") })
	pane := "%0"
	for range 50 {
		if c, _ := s.Capture(pane); strings.Contains(c, "❯") || strings.Contains(c, "↑") { // a scrolled box hides ❯
			return s, pane
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fake claude never drew its box")
	return s, pane
}

func screenOf(s tmux.Server, pane string) string { c, _ := s.Capture(pane); return c }

func TestSubmit(t *testing.T) {
	s, pane := fakePane(t)
	n, err := Submit(s, pane, "hello; world;", fast)
	if err != nil || n != 1 {
		t.Fatalf("Submit = %d, %v; want 1, nil", n, err)
	}
	if c := screenOf(s, pane); !strings.Contains(c, "SUBMITTED: hello; world;") {
		t.Fatalf("not submitted:\n%s", c)
	}
}

func TestSubmitRetriesSwallowedEnter(t *testing.T) {
	s, pane := fakePane(t, "FAKE_SWALLOW=2")
	n, err := Submit(s, pane, "retry me", fast)
	if err != nil || n != 3 {
		t.Fatalf("Submit = %d, %v; want 3, nil", n, err)
	}
}

func TestSubmitGivesUp(t *testing.T) {
	s, pane := fakePane(t, "FAKE_SWALLOW=99")
	o := fast
	o.Attempts = 3
	if _, err := Submit(s, pane, "never goes", o); !errors.Is(err, ErrNotSubmitted) {
		t.Fatalf("err = %v, want ErrNotSubmitted", err)
	}
}

func TestSubmitNeverPressesForHeadsUp(t *testing.T) {
	// A real notice eats keys: the echo check fails, nothing is submitted,
	// and the error points at the notice. No "0" is pressed for it.
	s, pane := fakePane(t, "FAKE_HEADSUP=1")
	_, err := Submit(s, pane, "after notice", fast)
	if !errors.Is(err, ErrEcho) || !strings.Contains(err.Error(), "Heads up") {
		t.Fatalf("err = %v, want ErrEcho naming the notice", err)
	}
	if c := screenOf(s, pane); !strings.Contains(c, "0: Dismiss") || strings.Contains(c, "SUBMITTED") {
		t.Fatalf("notice was dismissed or text submitted:\n%s", c)
	}
}

func TestSubmitIgnoresDismissTextInConversation(t *testing.T) {
	// "0: Dismiss" in ordinary output must not cause a keypress.
	s, pane := fakePane(t, "FAKE_LOG='it printed 0: Dismiss earlier'")
	if _, err := Submit(s, pane, "hello", fast); err != nil {
		t.Fatal(err)
	}
	if c := screenOf(s, pane); !strings.Contains(c, "SUBMITTED: hello") {
		t.Fatalf("not submitted cleanly:\n%s", c)
	}
}

func TestSubmitRefusesDraft(t *testing.T) {
	s, pane := fakePane(t, "FAKE_DRAFT=half-typed")
	if _, err := Submit(s, pane, "mine", fast); !errors.Is(err, ErrDraft) {
		t.Fatalf("err = %v, want ErrDraft", err)
	}
	if c := screenOf(s, pane); !strings.Contains(c, "half-typed") || strings.Contains(c, "mine") {
		t.Fatalf("draft was touched:\n%s", c)
	}
}

func TestSubmitRetypesCollapsedPaste(t *testing.T) {
	s, pane := fakePane(t, "FAKE_COLLAPSE=2")
	text := "a message long enough to collapse"
	if _, err := Submit(s, pane, text, fast); err != nil {
		t.Fatal(err)
	}
	if c := screenOf(s, pane); !strings.Contains(c, "SUBMITTED: "+text) {
		t.Fatalf("not submitted intact:\n%s", c)
	}
}

func TestSubmitGivesUpOnPersistentCollapse(t *testing.T) {
	s, pane := fakePane(t, "FAKE_COLLAPSE=99")
	if _, err := Submit(s, pane, "a message long enough to collapse", fast); !errors.Is(err, ErrEcho) {
		t.Fatalf("err = %v, want ErrEcho", err)
	}
	if c := screenOf(s, pane); strings.Contains(c, "SUBMITTED") || strings.Contains(c, "[Pasted") {
		t.Fatalf("collapsed text was submitted or left in the box:\n%s", c)
	}
}

func TestSubmitRefusesNewline(t *testing.T) {
	if _, err := Submit(tmux.Server{}, "%0", "a\nb", fast); !errors.Is(err, ErrMultiline) {
		t.Fatalf("err = %v, want ErrMultiline", err)
	}
}

// A dialog on screen: typing would answer it, so nothing is typed.
func TestSubmitRefusesDialog(t *testing.T) {
	s, pane := fakePane(t, "FAKE_DIALOG=1")
	if _, err := Submit(s, pane, "1", fast); !errors.Is(err, ErrDialog) {
		t.Fatalf("Submit over a dialog: err = %v, want ErrDialog", err)
	}
	time.Sleep(100 * time.Millisecond)
	if c := screenOf(s, pane); strings.Contains(c, "DIALOG ANSWERED") {
		t.Fatalf("a key reached the dialog:\n%s", c)
	}
}

func TestSubmitIdleOnly(t *testing.T) {
	s, pane := fakePane(t, "FAKE_BUSY=1")
	o := fast
	o.IdleOnly = true
	if _, err := Submit(s, pane, "later", o); !errors.Is(err, ErrBusy) {
		t.Fatalf("IdleOnly into a busy pane: err = %v, want ErrBusy", err)
	}
	// Without IdleOnly, a busy pane takes the message (Claude Code queues it).
	if _, err := Submit(s, pane, "now", fast); err != nil {
		t.Fatalf("Submit into a busy pane: %v", err)
	}
}

// A turn that starts while the text is in the box (a background task's
// notification) must not get an Enter: Claude Code would queue the text,
// and a pasted slash command would arrive later as plain prompt text.
func TestSubmitStopsWhenTurnStartsMidTyping(t *testing.T) {
	for name, text := range map[string]string{"typed": "/compact keep x", "long": "/compact " + strings.Repeat("k", 900)} {
		s, pane := fakePane(t, "FAKE_BUSY_ON_INPUT=1")
		o := fast
		o.IdleOnly = true
		if _, err := Submit(s, pane, text, o); !errors.Is(err, ErrBusy) {
			t.Fatalf("%s: err = %v, want ErrBusy", name, err)
		}
		c := screenOf(s, pane)
		if strings.Contains(c, "SUBMITTED") || strings.Contains(c, "kkk") || strings.Contains(c, "Pasted text") {
			t.Fatalf("%s: text submitted or left in the box:\n%s", name, c)
		}
	}
}

func TestSubmitPastesLongText(t *testing.T) {
	s, pane := fakePane(t)
	text := "long; " + strings.Repeat("p", 900) + " END"
	n, err := Submit(s, pane, text, fast)
	if err != nil || n != 1 {
		t.Fatalf("Submit = %d, %v", n, err)
	}
	if c := screenOf(s, pane); !strings.Contains(strings.ReplaceAll(c, "\n", ""), "PASTED+SUBMITTED: "+text) {
		t.Fatalf("pasted text not submitted whole:\n%s", c)
	}
}

// A long slash command is typed, never pasted: Claude Code sends a pasted
// /compact to the model as prompt text instead of running it.
func TestSubmitTypesLongSlashCommand(t *testing.T) {
	s, pane := fakePane(t)
	text := "/compact Keep: " + strings.Repeat("k", 900) + " END"
	if _, err := Submit(s, pane, text, fast); err != nil {
		t.Fatal(err)
	}
	c := strings.ReplaceAll(screenOf(s, pane), "\n", "")
	if strings.Contains(c, "PASTED") || !strings.Contains(c, "SUBMITTED: "+text) {
		t.Fatalf("long slash command was pasted or not submitted whole:\n%s", c)
	}
}

// Two writers into one pane: the second waits for the first's lock and
// fails when the wait runs out.
func TestSubmitHoldsPaneLock(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, pane := fakePane(t)
	release, err := journal.LockWrites("k", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	o := fast
	o.Lock, o.LockWait = "k", 200*time.Millisecond
	if _, err := Submit(s, pane, "blocked", o); !errors.Is(err, journal.ErrLocked) {
		t.Fatalf("Submit with the lock held elsewhere: err = %v, want ErrLocked", err)
	}
	if c := screenOf(s, pane); strings.Contains(c, "blocked") {
		t.Fatalf("typed without the lock:\n%s", c)
	}
	release()
	if _, err := Submit(s, pane, "free", o); err != nil {
		t.Fatalf("Submit after release: %v", err)
	}
}

// An Enter that changes the box instead of submitting it (the slash menu
// completing "/mo") must not be followed by another Enter, which would run
// whatever the menu picked.
func TestSubmitStopsWhenEnterChangesBox(t *testing.T) {
	s, pane := fakePane(t, "FAKE_MENU=1")
	n, err := Submit(s, pane, "/mo", fast)
	if !errors.Is(err, ErrNotSubmitted) || n != 1 {
		t.Fatalf("Submit = %d, %v; want 1 press and ErrNotSubmitted", n, err)
	}
	if c := screenOf(s, pane); strings.Contains(c, "SUBMITTED") {
		t.Fatalf("a second Enter ran the menu's pick:\n%s", c)
	}
}

// An Enter that opens a confirmation dialog submitted the text: the box is
// gone, which is not a failure, and nothing answers the dialog.
func TestSubmitIntoConfirmation(t *testing.T) {
	s, pane := fakePane(t, "FAKE_CONFIRM=1")
	n, err := Submit(s, pane, "/model sonnet", fast)
	if err != nil || n != 1 {
		t.Fatalf("Submit = %d, %v", n, err)
	}
	if c := screenOf(s, pane); !strings.Contains(c, "Switch model?") {
		t.Fatalf("dialog answered or never opened:\n%s", c)
	}
}

// SubmitOwn presses Enter only on the caller's own text: a person's draft,
// even one starting the same way, stays in the box unsent.
func TestSubmitOwnNeverSubmitsSomeoneElsesDraft(t *testing.T) {
	s, pane := fakePane(t, "FAKE_DRAFT=/context-later")
	if _, ok, err := SubmitOwn(s, pane, "/context", Options{}); ok || err != nil || strings.Contains(screenOf(s, pane), "SUBMITTED") {
		t.Fatalf("ok=%v err=%v\n%s", ok, err, screenOf(s, pane))
	}
	s, pane = fakePane(t, "FAKE_DRAFT=/context")
	if _, ok, err := SubmitOwn(s, pane, "/context", Options{}); !ok || err != nil {
		t.Fatalf("its own text: ok=%v err=%v\n%s", ok, err, screenOf(s, pane))
	}
}

// In a short pane a long command scrolls the box and its ❯ row out of
// sight. Submit must still see its own text there and press Enter, and
// ClearOwn must still clear it, one wrapped row per C-u; a person's long
// draft stays untouched.
func TestScrolledBox(t *testing.T) {
	text := "/compact Keep: " + strings.Repeat("the loop id, the branch and the next steps; ", 6) + "END"
	s, pane := fakePane(t, "FAKE_BOX_ROWS=3")
	if _, err := Submit(s, pane, text, fast); err != nil {
		t.Fatalf("Submit into a scrolling box: %v\n%s", err, screenOf(s, pane))
	}
	if c := strings.ReplaceAll(screenOf(s, pane), "\n", ""); !strings.Contains(c, "SUBMITTED: "+text) {
		t.Fatalf("not submitted whole:\n%s", screenOf(s, pane))
	}

	own := strings.Repeat("ownkeys", 30)
	s, pane = fakePane(t, "FAKE_BOX_ROWS=3", "FAKE_DRAFT="+own)
	if !strings.Contains(screenOf(s, pane), "↑") {
		t.Fatalf("fake box didn't scroll:\n%s", screenOf(s, pane))
	}
	if !ClearOwn(s, pane, own) {
		t.Fatalf("ClearOwn left its own scrolled text:\n%s", screenOf(s, pane))
	}

	draft := strings.Repeat("persondraft", 20)
	s, pane = fakePane(t, "FAKE_BOX_ROWS=3", "FAKE_DRAFT="+draft)
	if _, err := Submit(s, pane, text, fast); !errors.Is(err, ErrDraft) {
		t.Fatalf("Submit over a person's scrolled draft = %v; want ErrDraft", err)
	}
	if ClearOwn(s, pane, own) {
		t.Fatal("ClearOwn cleared a person's draft")
	}
	if c := screenOf(s, pane); !strings.Contains(c, "↑") || !strings.Contains(c, "persondraft") {
		t.Fatalf("a person's draft was changed:\n%s", c)
	}
}
