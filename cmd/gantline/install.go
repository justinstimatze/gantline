package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/justinstimatze/gantline/internal/audit"
)

// hookEvents are the events `gantline install` registers. UserPromptSubmit
// and SessionStart run synchronously, because Claude Code shows a hook's
// stdout to the model only then (the queue footer, law 10); the rest are
// async so they never slow a turn.
var hookEvents = []string{"Notification", "PermissionRequest", "PostCompact", "PostModelSwitch", "PostToolUse",
	"PreCompact", "SessionEnd", "SessionStart", "Stop", "StopFailure", "UserPromptSubmit"}

func syncHook(ev string) bool { return ev == "UserPromptSubmit" || ev == "SessionStart" }

// installRecord remembers what install replaced, so uninstall can put it
// back exactly.
type installRecord struct {
	Settings   string          `json:"settings"`
	Backup     string          `json:"backup"`
	StatusLine json.RawMessage `json:"status_line"` // the statusLine object before install; null if none
	Time       time.Time       `json:"time"`
}

func recordPath() string { return filepath.Join(audit.StateDir(), "install.json") }

// installCmd handles `gantline install` and `gantline uninstall`.
func installCmd(args []string, remove bool) {
	name := "install"
	if remove {
		name = "uninstall"
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	home, _ := os.UserHomeDir()
	settings := fs.String("settings", filepath.Join(home, ".claude", "settings.json"), "settings file to change")
	dry := fs.Bool("dry-run", false, "print the new settings instead of writing them")
	claudeMD := fs.String("claude-md", "ask", "add gantline's lines to the CLAUDE.md file (install) or remove them (uninstall): ask, yes or no; install asks only on a terminal")
	mdPath := fs.String("claude-md-file", filepath.Join(home, ".claude", "CLAUDE.md"), "the CLAUDE.md file --claude-md changes")
	fs.Parse(args)
	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		fail(name, err)
	}
	if _, err := exec.LookPath("jq"); err != nil {
		fail(name, fmt.Errorf("needs jq: %w", err))
	}
	// Write through a symlink (dotfile-managed settings) to its target,
	// keeping the target's mode.
	if real, err := filepath.EvalSymlinks(*settings); err == nil {
		*settings = real
	}
	info, err := os.Stat(*settings)
	if err != nil {
		fail(name, err)
	}
	orig, err := os.ReadFile(*settings)
	if err != nil {
		fail(name, err)
	}
	var out []byte
	if remove {
		out, err = uninstallSettings(orig, *settings)
	} else {
		out, err = installSettings(orig, self)
	}
	if err != nil {
		fail(name, err)
	}
	if *dry {
		os.Stdout.Write(out)
		claudeMDStep(name, *mdPath, *claudeMD, remove, true)
		return
	}
	if bytes.Equal(out, orig) {
		fmt.Printf("gantline %s: %s already up to date\n", name, *settings)
		claudeMDStep(name, *mdPath, *claudeMD, remove, false)
		return
	}
	backup := filepath.Join(audit.StateDir(), fmt.Sprintf("settings.json.%s.%s.bak", name, time.Now().Format("20060102-150405")))
	if err := os.WriteFile(backup, orig, 0o600); err != nil {
		fail(name, err)
	}
	tmp := *settings + ".gantline-tmp"
	if err := os.WriteFile(tmp, out, info.Mode().Perm()); err != nil {
		fail(name, err)
	}
	if err := exec.Command("jq", "-e", ".hooks", tmp).Run(); err != nil && !remove {
		os.Remove(tmp)
		fail(name, fmt.Errorf("new settings failed `jq -e .hooks`; left %s untouched", *settings))
	}
	// Claude Code may have written the file since it was read: renaming
	// over that write would lose it.
	if now, err := os.ReadFile(*settings); err != nil || !bytes.Equal(now, orig) {
		os.Remove(tmp)
		fail(name, fmt.Errorf("%s changed while gantline was editing it; run %s again", *settings, name))
	}
	if err := os.Rename(tmp, *settings); err != nil {
		os.Remove(tmp)
		fail(name, err)
	}
	if !remove {
		// The record goes last, so a failed install never overwrites the
		// statusline a reinstall has to restore.
		var prev struct {
			StatusLine json.RawMessage `json:"statusLine"`
		}
		json.Unmarshal(orig, &prev)
		if old, err := readRecord(); err == nil && isGantlineStatusLine(prev.StatusLine) {
			prev.StatusLine = old.StatusLine // reinstall: keep the true original
		}
		rec, _ := json.MarshalIndent(installRecord{Settings: *settings, Backup: backup, StatusLine: prev.StatusLine, Time: time.Now()}, "", "  ")
		if err := os.WriteFile(recordPath(), rec, 0o600); err != nil {
			fail(name, fmt.Errorf("settings updated, but the install record wasn't written: %w", err))
		}
	}
	fmt.Printf("gantline %s: updated %s (backup %s)\n", name, *settings, backup)
	claudeMDStep(name, *mdPath, *claudeMD, remove, false)
}

func fail(name string, err error) {
	fmt.Fprintf(os.Stderr, "gantline %s: %v\n", name, err)
	os.Exit(1)
}

func readRecord() (installRecord, error) {
	var r installRecord
	data, err := os.ReadFile(recordPath())
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(data, &r)
}

func isGantlineStatusLine(raw json.RawMessage) bool {
	var sl struct {
		Command string `json:"command"`
	}
	json.Unmarshal(raw, &sl)
	return strings.Contains(sl.Command, "gantline statusline")
}

// jqFilter drops every hook entry whose command runs `gantline hook`, and
// any matcher group left empty, so install is idempotent and uninstall
// leaves the other hooks as they were.
const dropGantline = `
def gl: ((.command // "") | test("gantline(\"|')? hook( |$)"));
def strip: if .hooks then .hooks |= with_entries(.value |= (map(.hooks |= map(select(gl | not))) | map(select((.hooks | length) > 0)))) | .hooks |= with_entries(select((.value | length) > 0)) else . end;
`

// installSettings returns settings with gantline's hooks added and the
// statusline wrapped. jq does the edit so the file keeps its key order.
func installSettings(orig []byte, self string) ([]byte, error) {
	add := map[string]any{}
	for _, ev := range hookEvents {
		h := map[string]any{"type": "command", "command": shellQuote(self) + " hook", "timeout": 5}
		if !syncHook(ev) {
			h["async"] = true
		}
		add[ev] = map[string]any{"matcher": "", "hooks": []any{h}}
	}
	addJSON, _ := json.Marshal(add)
	var prev struct {
		StatusLine struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	json.Unmarshal(orig, &prev)
	wrap := shellQuote(self) + " statusline"
	switch {
	case strings.Contains(prev.StatusLine.Command, "gantline statusline"):
		wrap = "" // already wrapped
	case prev.StatusLine.Command != "":
		wrap += " -- sh -c " + shellQuote(prev.StatusLine.Command)
	}
	filter := dropGantline + `strip | .hooks = ((.hooks // {}) as $h | reduce ($add | keys_unsorted[]) as $ev ($h; .[$ev] = ((.[$ev] // []) + [$add[$ev]])))
| if $wrap != "" then .statusLine = ((.statusLine // {"type": "command"}) + {"command": $wrap}) else . end`
	return runJQ(orig, filter, "--argjson", "add", string(addJSON), "--arg", "wrap", wrap)
}

// uninstallSettings removes gantline's hooks and puts back the statusLine
// install recorded for this settings file. With no record for it, the
// wrapped command is read back out of gantline's own statusline.
func uninstallSettings(orig []byte, path string) ([]byte, error) {
	restore := "null"
	if r, err := readRecord(); err == nil && r.Settings == path && len(r.StatusLine) > 0 {
		restore = string(r.StatusLine)
	} else {
		var cur struct {
			StatusLine map[string]any `json:"statusLine"`
		}
		json.Unmarshal(orig, &cur)
		if c, ok := cur.StatusLine["command"].(string); ok {
			if inner, ok := unwrapStatusLine(c); ok {
				cur.StatusLine["command"] = inner
				b, _ := json.Marshal(cur.StatusLine)
				restore = string(b)
			}
		}
	}
	filter := dropGantline + `strip
| if ((.statusLine.command // "") | test("gantline statusline")) then (if $sl == null then del(.statusLine) else .statusLine = $sl end) else . end`
	return runJQ(orig, filter, "--argjson", "sl", restore)
}

func runJQ(in []byte, filter string, args ...string) ([]byte, error) {
	cmd := exec.Command("jq", append(append([]string{}, args...), filter)...)
	cmd.Stdin = bytes.NewReader(in)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("jq: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// shellQuote quotes s for sh, leaving plain paths bare.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// unwrapStatusLine returns the command installSettings wrapped: the
// argument of `<gantline> statusline -- sh -c '<cmd>'`.
func unwrapStatusLine(cmd string) (string, bool) {
	_, q, ok := strings.Cut(cmd, " statusline -- sh -c ")
	if !ok || q == "" {
		return "", false
	}
	if q[0] != '\'' {
		return q, true
	}
	if len(q) < 2 || q[len(q)-1] != '\'' {
		return "", false
	}
	return strings.ReplaceAll(q[1:len(q)-1], `'\''`, "'"), true
}
