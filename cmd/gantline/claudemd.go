package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justinstimatze/gantline/internal/audit"
)

// claudeMDSnippet tells a session that gantline exists. The README shows the
// same text; TestReadmeShowsClaudeMDSnippet keeps the two equal.
//
//go:embed claudemd.md
var claudeMDSnippet string

const (
	mdBegin = "<!-- gantline: added by `gantline install`; `gantline uninstall` removes this block -->"
	mdEnd   = "<!-- /gantline -->"
)

// addClaudeMD returns orig with gantline's block added or brought up to
// date. A file that already mentions `gantline q` outside a block is left
// alone, since someone wrote those lines by hand; note says why.
func addClaudeMD(orig []byte) (out []byte, note string) {
	block := mdBegin + "\n" + claudeMDSnippet + mdEnd + "\n"
	s := string(orig)
	if i := strings.Index(s, mdBegin); i >= 0 {
		j := strings.Index(s[i:], mdEnd)
		if j < 0 {
			return orig, "has gantline's opening marker but no closing one; left it alone"
		}
		end := i + j + len(mdEnd)
		if end < len(s) && s[end] == '\n' {
			end++
		}
		if s[i:end] == block {
			return orig, "already up to date"
		}
		return []byte(s[:i] + block + s[end:]), "updated gantline's lines"
	}
	if strings.Contains(s, "gantline q") {
		return orig, "already mentions `gantline q` in lines gantline didn't add; left them alone (compare with the README's snippet)"
	}
	// One newline after the file's own text, so removeClaudeMD can drop
	// exactly that one and give back the file as it was.
	sep := ""
	if len(s) > 0 {
		sep = "\n"
	}
	return []byte(s + sep + block), "added gantline's lines"
}

// removeClaudeMD returns orig without gantline's block, undoing exactly
// what addClaudeMD appended.
func removeClaudeMD(orig []byte) (out []byte, note string) {
	s := string(orig)
	i := strings.Index(s, mdBegin)
	if i < 0 {
		return orig, "has no block gantline added"
	}
	j := strings.Index(s[i:], mdEnd)
	if j < 0 {
		return orig, "has gantline's opening marker but no closing one; left it alone"
	}
	end := i + j + len(mdEnd)
	if end < len(s) && s[end] == '\n' {
		end++
	}
	before := s[:i]
	if end == len(s) { // appended last: drop the separator addClaudeMD put before it
		before = strings.TrimSuffix(before, "\n")
	}
	return []byte(before + s[end:]), "removed gantline's lines"
}

// claudeMDStep is install's and uninstall's CLAUDE.md half. mode is "ask",
// "yes" or "no". Install asks only on a terminal; uninstall removes only
// the block install added, so it doesn't ask.
func claudeMDStep(name, path, mode string, remove, dry bool) {
	if mode == "no" {
		return
	}
	if mode != "ask" && mode != "yes" {
		fail(name, fmt.Errorf("--claude-md takes ask, yes or no, not %q", mode))
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	orig, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		fail(name, err)
	}
	mdPerm := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mdPerm = info.Mode().Perm()
	}
	var out []byte
	var note string
	if remove {
		out, note = removeClaudeMD(orig)
	} else {
		out, note = addClaudeMD(orig)
	}
	shown := tildePath(path)
	if bytes.Equal(out, orig) {
		fmt.Printf("gantline %s: %s %s\n", name, shown, note)
		return
	}
	if dry {
		fmt.Printf("gantline %s: would change %s: %s\n", name, shown, note)
		return
	}
	if !remove && mode == "ask" {
		if !stdinIsTerminal() {
			fmt.Printf("gantline %s: left %s alone (not a terminal). Sessions learn about gantline from it: run with --claude-md=yes, or paste the README's lines.\n", name, shown)
			return
		}
		fmt.Printf("Add gantline's lines to %s, so sessions know gantline is there? [y/N] ", shown)
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Printf("gantline %s: left %s alone; the README lists the lines to add by hand.\n", name, shown)
			return
		}
	}
	backup := ""
	if orig != nil {
		backup = filepath.Join(audit.StateDir(), fmt.Sprintf("CLAUDE.md.%s.%s.bak", name, time.Now().Format("20060102-150405")))
		if err := os.WriteFile(backup, orig, 0o600); err != nil {
			fail(name, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fail(name, err)
	}
	tmp := path + ".gantline-tmp"
	if err := os.WriteFile(tmp, out, mdPerm); err != nil {
		fail(name, err)
	}
	if now, _ := os.ReadFile(path); !bytes.Equal(now, orig) {
		os.Remove(tmp)
		fail(name, fmt.Errorf("%s changed while gantline was editing it; run %s again", shown, name))
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		fail(name, err)
	}
	if backup != "" {
		fmt.Printf("gantline %s: %s in %s (backup %s)\n", name, note, shown, tildePath(backup))
	} else {
		fmt.Printf("gantline %s: %s in new file %s\n", name, note, shown)
	}
}

// stdinIsTerminal reports whether a person can answer a prompt. /dev/null
// is a character device too, and it is what a child process gets for
// stdin by default.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}
