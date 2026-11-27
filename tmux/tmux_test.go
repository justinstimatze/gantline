package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSendArgs(t *testing.T) {
	cases := []struct {
		text  string
		chunk int
		want  []string
	}{
		{"hello", 40, []string{"hello"}},
		{"abc;", 40, []string{"abc", `\;`}},
		{";", 40, []string{`\;`}},
		{"e;;", 40, []string{"e", `\;`, `\;`}},
		{"x;y", 40, []string{"x;y"}},
		{"abcdefgh", 4, []string{"abcd", "efgh"}},
		{"abc;defg", 4, []string{"abc", `\;`, "defg"}},
		{"ééé", 4, []string{"éé", "é"}}, // never split inside a rune
	}
	for _, c := range cases {
		got := SendArgs(c.text, c.chunk)
		if !slices.Equal(got, c.want) {
			t.Errorf("SendArgs(%q, %d) = %q, want %q", c.text, c.chunk, got, c.want)
		}
	}
}

// testServer starts a throwaway tmux server running `cat`, so whatever is
// typed and submitted is echoed back on screen.
func testServer(t *testing.T) (Server, string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	s := Server{Socket: fmt.Sprintf("gantline-test-%d-%d", os.Getpid(), time.Now().UnixNano())}
	if _, err := s.Run("new-session", "-d", "-x", "200", "-y", "40", "cat"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Run("kill-server") })
	out, err := s.Run("list-panes", "-F", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	return s, strings.TrimSpace(out)
}

func waitFor(t *testing.T, s Server, pane, want string) {
	t.Helper()
	var screen string
	for range 40 {
		screen, _ = s.Capture(pane)
		if strings.Contains(screen, want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q; last screen:\n%s", want, screen)
}

func TestSubmitDeliversLiterally(t *testing.T) {
	s, pane := testServer(t)
	fast := TypeOptions{ChunkBytes: 8, Gap: time.Millisecond}
	text := `/compact keep a;b; and C:\dir\; and done;;`
	if err := s.Submit(pane, text, fast); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, pane, text)
}

func TestRefusesInCopyMode(t *testing.T) {
	s, pane := testServer(t)
	if _, err := s.Run("copy-mode", "-t", pane); err != nil {
		t.Fatal(err)
	}
	err := s.Type(pane, "should not appear", TypeOptions{})
	if !errors.Is(err, ErrPaneInMode) {
		t.Fatalf("Type in copy mode: got %v, want ErrPaneInMode", err)
	}
	if err := s.Enter(pane); !errors.Is(err, ErrPaneInMode) {
		t.Fatalf("Enter in copy mode: got %v, want ErrPaneInMode", err)
	}
	s.Run("send-keys", "-t", pane, "-X", "cancel")
	screen, _ := s.Capture(pane)
	if strings.Contains(screen, "should not appear") {
		t.Fatalf("text reached the pane despite copy mode:\n%s", screen)
	}
}

func TestPasteDeliversLiterally(t *testing.T) {
	s, pane := testServer(t)
	text := `paste: a;b; C:\dir\; "quoted" $HOME ` + strings.Repeat("x", 300) + " END"
	if err := s.Paste(pane, text); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, pane, "END")
	// The buffer is deleted after pasting.
	if out, _ := s.Run("list-buffers", "-F", "#{buffer_name}"); strings.Contains(out, "gantline-") {
		t.Fatalf("paste buffer left behind: %q", out)
	}
	screen, _ := s.CaptureScroll(pane, 0, false)
	if !strings.Contains(strings.ReplaceAll(screen, "\n", ""), text) {
		t.Fatalf("pasted text altered; screen:\n%s", screen)
	}
}

func TestPasteRefusesInCopyMode(t *testing.T) {
	s, pane := testServer(t)
	s.Run("copy-mode", "-t", pane)
	if err := s.Paste(pane, "nope"); !errors.Is(err, ErrPaneInMode) {
		t.Fatalf("Paste in copy mode: got %v, want ErrPaneInMode", err)
	}
	s.Run("send-keys", "-t", pane, "-X", "cancel")
}
