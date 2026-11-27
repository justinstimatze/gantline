package screen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Synthetic screens with the structure of real 2.1.332 captures. Real
// captures stay local and never enter git.
const rule = "────────────────────────────────────────"

func box(lines ...string) string {
	s := "  Claude Code\n\n" + rule + "\n"
	for _, l := range lines {
		s += l + "\n"
	}
	return s + rule + "\n  Opus · project:main\n  ⏵⏵ auto mode on (shift+tab to cycle)\n"
}

func TestInput(t *testing.T) {
	cases := []struct {
		name, capture, want string
		found               bool
	}{
		{"placeholder", box("❯ Try \"refactor <filepath>\""), "", true},
		{"empty", box("❯ "), "", true},
		{"queued placeholder", box("❯ Press ↑ to edit queued messages"), "", true},
		{"draft", box("❯ fix the test"), "fix the test", true},
		{"wrapped draft", box("❯ m000.m001.", "  m002.m003."), "m000.m001.m002.m003.", true},
		{"plain-space marker", box("❯ hello"), "hello", true},
		{"no box", "just a shell prompt $ \n", "", false},
		{"dialog, prompt echo above", "❯ Run the bash command: touch f\n" + rule + "\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n", "", false},
	}
	for _, c := range cases {
		got, found := Input(c.capture)
		if got != c.want || found != c.found {
			t.Errorf("%s: Input = %q, %v; want %q, %v", c.name, got, found, c.want, c.found)
		}
	}
}

func TestHeadsUp(t *testing.T) {
	if !HeadsUp(box("❯ ") + "  Heads up: something changed\n  0: Dismiss\n") {
		t.Error("HeadsUp missed a notice")
	}
	if HeadsUp(box("❯ ")) {
		t.Error("HeadsUp fired on a plain screen")
	}
}

func TestPastedAndSameText(t *testing.T) {
	if !Pasted("[Pasted text #3]") || !Pasted("[Pasted text #3 +12 lines]") || Pasted("pasted text") {
		t.Error("Pasted misclassified")
	}
	// A space on a wrap point is not drawn, so the box reads it back without.
	if !SameText("padNNNpadNNN", "padNNN padNNN") || SameText("abc", "abd") {
		t.Error("SameText misjudged")
	}
}

func TestBusy(t *testing.T) {
	busy := box("❯\u00a0") + "  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt\n"
	if !Busy(busy) {
		t.Error("Busy missed the busy mode line")
	}
	if Busy(box("❯\u00a0")) {
		t.Error("Busy fired on an idle screen")
	}
	// The phrase in conversation output above the box must not count.
	talk := "  the docs say press esc to interrupt\n" + box("❯\u00a0")
	if Busy(talk) {
		t.Error("Busy matched conversation text")
	}
}

// TestBusyOnLocalCorpus checks Busy against real captures when the local
// corpus exists. Real captures never enter git, so this skips elsewhere.
func TestBusyOnLocalCorpus(t *testing.T) {
	home, _ := os.UserHomeDir()
	files, _ := filepath.Glob(filepath.Join(home, ".local/state/gantline/corpus/*/*.txt"))
	if len(files) == 0 {
		t.Skip("no local corpus")
	}
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	for _, f := range files {
		data, _ := os.ReadFile(f)
		name := filepath.Base(f)
		want := strings.HasPrefix(name, "busy-running")
		if !strings.HasPrefix(name, "busy-running") && !strings.HasPrefix(name, "idle-") {
			continue
		}
		if got := Busy(ansi.ReplaceAllString(string(data), "")); got != want {
			t.Errorf("%s: Busy = %v, want %v", name, got, want)
		}
	}
}

// A background shell pushes "esc to interrupt" off the end of the mode line.
func TestBusyWithBackgroundShell(t *testing.T) {
	if !Busy(box("❯ ") + "  ⏵⏵ auto mode on · 1 shell · esc to interrupt · ↓ to manage\n") {
		t.Error("Busy missed a mid-line esc to interrupt")
	}
	if Busy(box("❯ ") + "  ⏵⏵ auto mode on · 1 shell · ↓ to manage\n") {
		t.Error("Busy fired on an idle line with a background shell")
	}
}

// Background agents listed under the mode line push it out of the last
// three lines; Busy still reads it.
func TestBusyWithAgentList(t *testing.T) {
	if !Busy(agentListScreen) {
		t.Fatal("busy screen with an agent list read as idle")
	}
	idle := strings.NewReplacer("esc to interrupt · ", "", "✻ Transmuting… (1m 7s)", "✻ Worked for 1m 7s").Replace(agentListScreen)
	if Busy(idle) {
		t.Fatal("idle screen with an agent list read as busy")
	}
	if in, ok := Input(agentListScreen); !ok || in != "" {
		t.Fatalf("Input = %q, %v", in, ok)
	}
}

// A 56-column pane cuts the mode line: "· esc…" for a running turn, or
// before the segment entirely, where only the spinner above the box shows it.
func TestBusyNarrowPane(t *testing.T) {
	box := "\n" + strings.Repeat("─", 30) + " demo ─\n❯\u00a0\n" + strings.Repeat("─", 40) + "\n  Opus 5.5 · demo:feature/long-branch-…\n"
	cases := []struct {
		name, screen string
		busy         bool
	}{
		{"cut segment", "● Running 1 shell command…\n\n✢ Catapulting… (1m 56s · thinking)" + box + "  ⏵⏵ bypass permissions on · 4 background tasks · esc…\n", true},
		{"cut before segment", "✢ Catapulting… (1m 56s · thinking)" + box + "  ⏵⏵ bypass permissions on · 4 backgr…\n", true},
		{"idle, cut", "✻ Worked for 3s · done 9:00 PM" + box + "  ⏵⏵ bypass permissions on · 4 background tasks · ↓ to…\n", false},
		{"idle, two letters", "✻ Worked for 3s" + box + "  ⏵⏵ bypass permissions on · es…\n", false},
	}
	for _, c := range cases {
		if got := Busy(c.screen); got != c.busy {
			t.Errorf("%s: Busy = %v, want %v", c.name, got, c.busy)
		}
	}
}

func TestHasSegmentCut(t *testing.T) {
	for line, want := range map[string]bool{
		"⏵⏵ bypass permissions on · 4 background tasks · esc…": true,
		"⏵⏵ bypass permissions on · esc to interrupt · ↓ to…":  true,
		"⏵⏵ bypass permissions on · es…":                       false,
		"⏵⏵ bypass permissions on · esc… · ↓ to manage":        false,
	} {
		if got := hasSegment(line, "esc to interrupt"); got != want {
			t.Errorf("hasSegment(%q) = %v, want %v", line, got, want)
		}
	}
}

// A statusline that prints nothing leaves a blank row between the box and
// the mode line; the mode line is still found, and busy read from it.
func TestFooterAfterEmptyStatusline(t *testing.T) {
	rule := strings.Repeat("─", 56)
	c := strings.Join([]string{"● ok", "", rule, "❯ ", rule, "", "  ⏵⏵ auto mode on · esc to interrupt", "", "  ● main"}, "\n")
	l := Cut(c)
	if got, _, _ := l.Region("mode"); len(got) != 1 || !strings.Contains(got[0], "auto mode on") {
		t.Fatalf("mode = %q", got)
	}
	if got, _, _ := l.Region("status"); len(got) != 0 {
		t.Fatalf("status = %q, want none", got)
	}
	if !Busy(c) {
		t.Fatal("esc to interrupt under an empty statusline not read as busy")
	}
}

// A box taller than a short pane allows: the ❯ row scrolls away under a
// "─── ↑ N more ───" rule (2.1.332 in a 16-row pane), and the rows left
// show only the end of the text.
func TestScrolledBox(t *testing.T) {
	const up = "─── ↑ 2 more ──────────────────────────────── Jupiter ─"
	const down = "─── ↓ 1 more ──────────────────────────────────────────"
	typed := "/compact Keep: the loop's dispatch id, the branch, three unpushed commits and where the next steps are written."
	scrolled := "  Claude Code\n\n" + up + "\n  three unpushed commits and where the\n  next steps are written.\n" + rule +
		"\n  Opus · project:main\n  ⏵⏵ auto mode on · 2 background tasks\n"
	in, found := Input(scrolled)
	if !found || in != Hidden+"three unpushed commits and where thenext steps are written." {
		t.Fatalf("Input = %q, %v; want the visible rows after Hidden", in, found)
	}
	if !SameText(in, typed) {
		t.Errorf("SameText(%q, typed) = false; the rows shown are the end of typed", in)
	}
	for _, other := range []string{"a person's own draft", "next steps are written. and more", "three unpushed commits and where the next steps are written."} {
		if SameText(in, other) {
			t.Errorf("SameText(%q, %q) = true; want false", in, other)
		}
	}
	if Busy(scrolled) {
		t.Error("idle scrolled box read as busy")
	}
	if !Busy(strings.Replace(scrolled, "2 background tasks", "2 background tasks · esc to interrupt", 1)) {
		t.Error("busy scrolled box read as idle: the footer under it wasn't read")
	}

	// The cursor near the start: ❯ shows, rows below are hidden.
	top := box("❯ /compact Keep: the loop's dispatch id,")
	top = strings.Replace(top, "id,\n"+rule, "id,\n"+down, 1)
	in, found = Input(top)
	if !found || !strings.HasSuffix(in, Hidden) || !SameText(in, typed) {
		t.Errorf("Input = %q, %v; want the start of typed then Hidden", in, found)
	}
	if !isScrollRule(up, "↑") || isScrollRule(rule, "↑") || isScrollRule("─── ↑ more ────────────────────────────", "↑") {
		t.Error("isScrollRule misreads a rule")
	}
}
