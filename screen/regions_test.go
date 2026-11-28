package screen

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Synthetic screens shaped like real 2.1.332 captures.
var (
	idleScreen = "  older output\n" +
		"❯ /mcp reconnect echomcp\n" +
		"  ⎿  Reconnected to echomcp\n" +
		rule + "\n" +
		"❯  \n" +
		rule + "\n" +
		"  Sonnet 5.5 · p0 · ctx8%\n" +
		"  ⏵⏵ auto mode on · 1 shell · ↓ to manage\n\n\n"
	compactingScreen = "❯ /compact keep: step 2\n" +
		"* Compacting conversation… (5s · ↓ 437 tokens)\n" +
		rule + "\n❯ \n" + rule + "\n" +
		"  Sonnet 5.5 · p0 · ctx8%\n" +
		"  ⏵⏵ auto mode on · 1 shell · esc to interrupt · ↓ to manage\n"
	dialogScreen = "❯ Run the bash command: touch f\n" +
		"  ⎿  $ touch f\n" +
		rule + "\n" +
		" Bash command\n" +
		"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌\n" +
		" touch f\n" +
		"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌\n" +
		" Do you want to proceed?\n" +
		" ❯ 1. Yes\n" +
		"   2. No\n" +
		" Esc to cancel · Tab to amend\n"
	wrappedScreen = "❯ first\n✻ Worked for 3s · done 9:00 PM\n" + rule + "\n" +
		"❯ m000.m001.\n  m002.m003.\n" + rule + "\n  Haiku 5.5\n  ⏵⏵ auto mode on\n"
)

func TestCut(t *testing.T) {
	cases := []struct {
		name, screen                    string
		box, mode, status, last, dialog string
		spinner                         string
		fingerprints                    []string
	}{
		{"idle", idleScreen, "❯", "⏵⏵ auto mode on · 1 shell · ↓ to manage", "  Sonnet 5.5 · p0 · ctx8%",
			"❯ /mcp reconnect echomcp\n  ⎿  Reconnected to echomcp", "", "", nil},
		{"compacting", compactingScreen, "❯", "  ⏵⏵ auto mode on · 1 shell · esc to interrupt · ↓ to manage", "  Sonnet 5.5 · p0 · ctx8%",
			"❯ /compact keep: step 2\n* Compacting conversation… (5s · ↓ 437 tokens)", "",
			"* Compacting conversation… (5s · ↓ 437 tokens)", []string{"busy", "compacting"}},
		{"dialog", dialogScreen, "", "", "", "❯ Run the bash command: touch f\n  ⎿  $ touch f",
			strings.Join(strings.Split(strings.TrimSuffix(dialogScreen[strings.Index(dialogScreen, rule)+len(rule)+1:], "\n"), "\n"), "\n"),
			"", []string{"permission_dialog"}},
		{"wrapped box", wrappedScreen, "❯ m000.m001.\n  m002.m003.", "  ⏵⏵ auto mode on", "  Haiku 5.5",
			"❯ first\n✻ Worked for 3s · done 9:00 PM", "", "", nil},
		{"labelled rule, agent list", agentListScreen, "❯", "⏵⏵ bypass permissions on · 3 background tasks · esc to interrupt · ↓ to manage",
			"  Opus 5.5 · repo · ctx23%", "❯ do the thing\n✻ Transmuting… (1m 7s)", "", "✻ Transmuting… (1m 7s)", []string{"busy"}},
		{"empty", "", "", "", "", "", "", "", nil},
		{"shell", "$ ls\nfoo bar\n$\n", "", "", "", "$ ls\nfoo bar\n$", "", "", nil},
	}
	for _, c := range cases {
		l := Cut(c.screen)
		got := map[string]string{"box": l.Text("box"), "mode": l.Text("mode"), "status": l.Text("status"),
			"last": l.Text("last"), "dialog": l.Text("dialog"), "spinner": l.Text("spinner")}
		want := map[string]string{"box": c.box, "mode": c.mode, "status": c.status,
			"last": c.last, "dialog": c.dialog, "spinner": c.spinner}
		for k := range want {
			if strings.TrimSpace(got[k]) != strings.TrimSpace(want[k]) {
				t.Errorf("%s: region %s = %q, want %q", c.name, k, got[k], want[k])
			}
		}
		if ids := Match(l); !reflect.DeepEqual(ids, c.fingerprints) {
			t.Errorf("%s: Match = %v, want %v", c.name, ids, c.fingerprints)
		}
	}
	if _, _, ok := Cut(idleScreen).Region("nope"); ok {
		t.Error("an unknown region was accepted")
	}
}

func TestOptions(t *testing.T) {
	l := Cut(dialogScreen)
	lines, _, _ := l.Region("dialog")
	want := []Option{{1, "Yes", true}, {2, "No", false}}
	if got := Options(lines); !reflect.DeepEqual(got, want) {
		t.Fatalf("Options = %+v", got)
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[38;5;246mRan\x1b[0m \x1b]8;id=x;https://e.com\x1b\\link\x1b]8;;\x1b\\"
	if got := StripANSI(in); got != "Ran link" {
		t.Fatalf("StripANSI = %q", got)
	}
}

// Every fingerprint must have a synthetic screen above that it matches, so
// the table can't grow an entry nothing in git exercises.
func TestEveryFingerprintHasSyntheticCase(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range []string{idleScreen, compactingScreen, dialogScreen, wrappedScreen, trustScreen, modelSwitchScreen, agentListScreen} {
		for _, id := range Match(Cut(s)) {
			seen[id] = true
		}
	}
	for _, f := range Fingerprints {
		if !seen[f.ID] {
			t.Errorf("fingerprint %s matches no synthetic screen", f.ID)
		}
	}
}

// The trust dialog with the cursor moved to "Yes", as on a macOS runner.
func TestTrustDialogCursorOnYes(t *testing.T) {
	yes := strings.Replace(strings.Replace(trustScreen, " ❯ No, exit", "   No, exit", 1), "   Yes, I trust", " ❯ Yes, I trust", 1)
	if yes == trustScreen {
		t.Fatal("fixture unchanged")
	}
	if got := Dialog(Cut(yes)); got != "trust_folder" {
		t.Fatalf("Dialog = %q, want trust_folder", got)
	}
}

var modelSwitchScreen = "● done\n✻ Cogitated for 14s · done 11:19 PM\n" + strings.Repeat("▔", 40) + "\n" +
	"  Switch model?\n  Your next response will be slower and use more tokens\n\n" +
	"  ❯ 1. Yes, switch to Sonnet 5.5\n    2. No, go back\n  Enter to confirm · Esc to cancel\n"

var agentListScreen = "❯ do the thing\n✻ Transmuting… (1m 7s)\n\n" + strings.Repeat("─", 40) + " demo ─\n❯\u00a0\n" + rule + "\n" +
	"  Opus 5.5 · repo · ctx23%\n  ⏵⏵ bypass permissions on · 3 background tasks · esc to interrupt · ↓ to manage\n\n" +
	"  ● main\n  ◯ general-purpose  Panel: one\n  ◯ general-purpose  Panel: two\n  ◯ general-purpose  Panel: three\n"

var trustScreen = rule + "\n Accessing workspace:\n /home/u/p\n ❯ No, exit\n   Yes, I trust this folder\n Enter to confirm · Esc to cancel\n"

// corpusDir is the local corpus for one Claude Code version, or "".
func corpusDir(version string) string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "gantline", "corpus", version)
	if _, err := os.Stat(dir); err != nil {
		return ""
	}
	return dir
}

func loadFrame(t *testing.T, path string) Layout {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return Cut(StripANSI(string(data)))
}

// TestFingerprintsVerified enforces law 6 against the local corpus: every
// fingerprint that say/q act on names real captures, each of those matches,
// and no capture listed under Never matches. Skips without a corpus.
func TestFingerprintsVerified(t *testing.T) {
	dir := corpusDir("2.1.332")
	if dir == "" {
		t.Skip("no local corpus")
	}
	for _, f := range Fingerprints {
		if f.Acts && len(f.Verified) == 0 {
			t.Errorf("%s acts on the screen but names no verifying capture", f.ID)
		}
		for _, g := range f.Verified {
			files, _ := filepath.Glob(filepath.Join(dir, g))
			if len(files) == 0 {
				t.Errorf("%s: verifying glob %s matches no capture", f.ID, g)
			}
			for _, p := range files {
				if !slices.Contains(Match(loadFrame(t, p)), f.ID) {
					t.Errorf("%s: does not match its verifying capture %s", f.ID, filepath.Base(p))
				}
			}
		}
		for _, g := range f.Never {
			files, _ := filepath.Glob(filepath.Join(dir, g))
			for _, p := range files {
				if slices.Contains(Match(loadFrame(t, p)), f.ID) {
					t.Errorf("%s: matches %s, which it must not", f.ID, filepath.Base(p))
				}
			}
		}
	}
}

// TestCorpusLayout checks the cut on every local capture: each frame has
// exactly one of a box or a dialog, and the two independent busy signals
// (the mode line and the spinner above the box) agree.
func TestCorpusLayout(t *testing.T) {
	dir := corpusDir("2.1.332")
	if dir == "" {
		t.Skip("no local corpus")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
	more, _ := filepath.Glob(filepath.Join(dir, "frames", "*.txt"))
	for _, p := range append(files, more...) {
		l := loadFrame(t, p)
		name := filepath.Base(p)
		if l.Box.Empty() == l.Dialog.Empty() {
			t.Errorf("%s: box %v and dialog %v: want exactly one", name, l.Box, l.Dialog)
			continue
		}
		if l.Box.Empty() {
			continue
		}
		busy := slices.Contains(Match(l), "busy")
		if busy != (l.Spinner >= 0) {
			t.Errorf("%s: mode line busy=%v but spinner line %q", name, busy, l.Text("spinner"))
		}
	}
}

func TestModelSwitchDialog(t *testing.T) {
	l := Cut(modelSwitchScreen)
	if Dialog(l) != "model_switch_confirm" || DialogTitle(l) != "Switch model?" || !l.Box.Empty() {
		t.Fatalf("Dialog = %q, title %q, box %v", Dialog(l), DialogTitle(l), l.Box)
	}
}

func TestPlainDropsSuggestion(t *testing.T) {
	ghost := rule + "\n\x1b[39m❯ \x1b[2mshow me the step output files\x1b[0m\n" + rule + "\n  ⏵⏵ auto mode on\n"
	if in, ok := Input(Plain(ghost)); !ok || in != "" {
		t.Fatalf("suggestion read as draft %q (found %v)", in, ok)
	}
	typed := rule + "\n\x1b[39m❯ \x1b[38;5;153m/model\x1b[39m sonnet\n" + rule + "\n  ⏵⏵ auto mode on\n"
	if in, _ := Input(Plain(typed)); in != "/model sonnet" {
		t.Fatalf("typed draft = %q", in)
	}
}

// A /compact taller than a 56x16 pane, drawn whole by Claude Code 2.1.332
// under tmux 3.8 on macOS: the box's top rule and ❯ line are in tmux's
// history, and the screen shows only its tail.
func TestOverflowedBox(t *testing.T) {
	keep := "Keep the step files, the queue ids and the scratch project path. "
	typed := "/compact keep: this is the gantline e2e test. " + strings.Repeat(keep, 14)
	screenText := "  step files, the queue ids and the scratch project\n" +
		"  path. Keep the step files, the queue ids and the\n" +
		"  scratch project path. Keep the step files, the queue\n" +
		"  ids and the scratch project path. \n" +
		strings.Repeat("─", 56) + "\n  ⏵⏵ auto mode on (shift+tab to cycle)\n"
	in, found := Input(screenText)
	if !found || !strings.HasPrefix(in, Hidden) {
		t.Fatalf("Input = %q, %v; want the box's tail after Hidden", in, found)
	}
	if !SameText(in, typed) {
		t.Fatalf("SameText(%q, typed) = false", in)
	}
	if SameText(in, typed+"x") {
		t.Fatal("SameText matched text with a different end")
	}
	if Busy(screenText) {
		t.Fatal("Busy on an idle overflowed box")
	}
	// The same shape with options under the rule is a dialog, not a box.
	dialog := "  some wrapped output line\n" + strings.Repeat("─", 56) + "\n  ❯ 1. Yes\n    2. No\n  Enter to confirm · Esc to cancel\n"
	if _, found := Input(dialog); found {
		t.Fatal("a dialog read as an overflowed box")
	}
	// A permission dialog whose last option names shift+tab.
	perm := "  some wrapped output line\n" + strings.Repeat("─", 56) + "\n  Do you want to make this edit?\n  ❯ 1. Yes\n    2. Yes, allow all edits during this session (shift+tab)\n"
	if _, found := Input(perm); found {
		t.Fatal("a permission dialog naming shift+tab read as an overflowed box")
	}
	// Conversation output starts at column 0 ("⏺ ..."), so a screen whose
	// box is simply missing never reads as an overflowed one.
	if _, found := Input("⏺ done\n  more\n" + strings.Repeat("─", 56) + "\n  ⏵⏵ auto mode on (shift+tab to cycle)\n"); found {
		t.Fatal("conversation output read as an overflowed box")
	}
}
