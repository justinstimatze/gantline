package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The README's step 4 shows exactly the lines install adds.
func TestReadmeShowsClaudeMDSnippet(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(readme), "\n   ```markdown\n")
	block, _, ok2 := strings.Cut(after, "\n   ```\n")
	if !ok || !ok2 {
		t.Fatal("README has no indented ```markdown block")
	}
	var lines []string
	for _, l := range strings.Split(block, "\n") {
		lines = append(lines, strings.TrimPrefix(l, "   "))
	}
	if got := strings.Join(lines, "\n") + "\n"; got != claudeMDSnippet {
		t.Errorf("README snippet and cmd/gantline/claudemd.md differ:\nREADME:\n%s\nembedded:\n%s", got, claudeMDSnippet)
	}
}

func TestClaudeMDBlock(t *testing.T) {
	for _, orig := range []string{"", "# Mine\n\n- a rule\n", "no trailing newline"} {
		added, note := addClaudeMD([]byte(orig))
		if !strings.Contains(string(added), claudeMDSnippet) || !strings.HasPrefix(string(added), orig) {
			t.Errorf("add to %q (%s) gave:\n%s", orig, note, added)
		}
		if again, _ := addClaudeMD(added); string(again) != string(added) {
			t.Errorf("add to %q is not idempotent", orig)
		}
		if back, _ := removeClaudeMD(added); string(back) != orig {
			t.Errorf("remove after add to %q gave %q", orig, back)
		}
	}

	// A stale block is replaced in place; text after it survives.
	stale := "top\n" + mdBegin + "\n- old gantline line\n" + mdEnd + "\nbottom\n"
	got, _ := addClaudeMD([]byte(stale))
	if want := "top\n" + mdBegin + "\n" + claudeMDSnippet + mdEnd + "\nbottom\n"; string(got) != want {
		t.Errorf("update of a stale block gave:\n%s", got)
	}
	if back, _ := removeClaudeMD(got); string(back) != "top\nbottom\n" {
		t.Errorf("remove of a middle block gave %q", back)
	}

	// Lines someone wrote by hand are theirs: no second copy, no removal.
	hand := "- queue /compact with `gantline q '/compact'`\n"
	if got, note := addClaudeMD([]byte(hand)); string(got) != hand || !strings.Contains(note, "left them alone") {
		t.Errorf("add over hand-written lines: %q (%s)", got, note)
	}
	if got, _ := removeClaudeMD([]byte(hand)); string(got) != hand {
		t.Errorf("remove touched hand-written lines: %q", got)
	}
	broken := mdBegin + "\n- no end marker\n"
	if got, _ := addClaudeMD([]byte(broken)); string(got) != broken {
		t.Error("add rewrote a block with no end marker")
	}
	if got, _ := removeClaudeMD([]byte(broken)); string(got) != broken {
		t.Error("remove cut a block with no end marker")
	}
}

// Through the binary: --claude-md=yes adds the block, the default (ask)
// leaves the file alone when stdin isn't a terminal, and uninstall puts
// the file back byte for byte.
func TestInstallEditsClaudeMD(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	bin := buildGantline(t)
	home, state := t.TempDir(), t.TempDir()
	settings := filepath.Join(home, "settings.json")
	md := filepath.Join(home, "CLAUDE.md")
	const mine = "# My rules\n\n- be brief\n"
	os.WriteFile(settings, []byte(`{"model": "opus"}`), 0o600)
	os.WriteFile(md, []byte(mine), 0o640)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = []string{"HOME=" + home, "XDG_STATE_HOME=" + state, "PATH=" + os.Getenv("PATH")}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	read := func() string { b, _ := os.ReadFile(md); return string(b) }

	if out := run("install", "--settings", settings, "--claude-md-file", md); read() != mine || !strings.Contains(out, "not a terminal") {
		t.Errorf("install without a terminal changed CLAUDE.md or didn't say why:\n%s", out)
	}
	run("install", "--settings", settings, "--claude-md-file", md, "--claude-md=yes")
	if got := read(); !strings.HasPrefix(got, mine) || !strings.Contains(got, claudeMDSnippet) {
		t.Errorf("install --claude-md=yes gave:\n%s", got)
	}
	if fi, _ := os.Stat(md); fi.Mode().Perm() != 0o640 {
		t.Errorf("CLAUDE.md mode %v, want 0640 kept", fi.Mode().Perm())
	}
	if out := run("install", "--settings", settings, "--claude-md-file", md, "--claude-md=yes"); !strings.Contains(out, "already up to date") {
		t.Errorf("second install:\n%s", out)
	}
	run("uninstall", "--settings", settings, "--claude-md-file", md)
	if got := read(); got != mine {
		t.Errorf("uninstall left CLAUDE.md as %q, want %q", got, mine)
	}
	if out := run("install", "--settings", settings, "--claude-md-file", filepath.Join(home, "new", "CLAUDE.md"), "--claude-md=yes"); !strings.Contains(out, "new file") {
		t.Errorf("install into a missing CLAUDE.md:\n%s", out)
	}
}
