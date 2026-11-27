//go:build realclaude

package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/queue"
	"github.com/justinstimatze/gantline/screen"
	"github.com/justinstimatze/gantline/tmux"
)

// Steps live in files in the scratch project, so each queued follow-up only
// has to say "read steps/N.txt": the instructions survive the session's own
// /compact without depending on what the summary kept.
var steps = []string{
	"Run `gantline status > step1.txt`. Then run `gantline q --then 'E2E: read steps/2.txt and do exactly what it says.' /context`. Then end your turn by replying only: ok",
	"Run `gantline q --show q1 > ctx.txt`, `gantline screen --scrollback 300 --find '/[0-9.]+[km] tokens' > panel.txt` and `gantline screen --region mode > screen.txt`. Then run `gantline q --then 'E2E: read steps/3.txt and do exactly what it says.' \"$(cat compact.txt)\"`. Then end your turn by replying only: ok",
	"Run `gantline status --json > step3.json`. Then run `gantline q --then 'E2E: read steps/4.txt and do exactly what it says.' '/mcp reconnect echomcp'`. Then end your turn by replying only: ok",
	"Run `gantline status --json > step4.json`. Then run `gantline model --then 'E2E: read steps/5.txt and do exactly what it says.' sonnet`. Then end your turn by replying only: ok",
	"Run `gantline status --json > step5.json`, then run `touch done.txt`. Then reply only: done",
}

// compactCmd is the /compact the session queues from compact.txt. It runs
// past 900 bytes, as real self-compactions with long keep lists do: say once
// pasted text that long, and Claude Code sent a pasted /compact to the model
// as prompt text instead of running it.
var compactCmd = "/compact keep: this is the gantline e2e test; the remaining steps are in steps/*.txt." +
	strings.Repeat(" Keep the step files, the queue ids and the scratch project path.", 14)

const prompt = "E2E TEST OF GANTLINE, automated. Use the Bash tool. Read steps/1.txt and do exactly what it says. Never ask questions."

func TestSelfControl(t *testing.T) {
	runs := 1
	if n, err := strconv.Atoi(os.Getenv("GANTLINE_E2E_RUNS")); err == nil && n > 0 {
		runs = n
	}
	bin := buildBinaries(t)
	guardServers(t)
	var took []time.Duration
	passed := 0
	for i := range runs {
		ok := t.Run(fmt.Sprintf("run%d", i+1), func(t *testing.T) {
			start := time.Now()
			selfControl(t, bin)
			took = append(took, time.Since(start))
		})
		if ok {
			passed++
		}
	}
	t.Logf("passed %d/%d; wall times %v", passed, runs, took)
}

// guardServers kills the tmux servers on SIGINT/SIGTERM and shortly before
// the test deadline: a -timeout panic or a signal exits without running
// cleanups, which would leave a live Haiku session behind.
func guardServers(t *testing.T) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	var guard <-chan time.Time
	if d, ok := t.Deadline(); ok {
		guard = time.After(time.Until(d) - 20*time.Second)
	}
	go func() {
		select {
		case s := <-sig:
			killServers()
			fmt.Fprintf(os.Stderr, "e2e: %v, tmux servers killed\n", s)
			os.Exit(1)
		case <-guard:
			killServers()
			fmt.Fprintln(os.Stderr, "e2e: near the test deadline, tmux servers killed")
		}
	}()
}

// buildBinaries builds gantline and the echo MCP server into a temp dir.
func buildBinaries(t *testing.T) string {
	bin := t.TempDir()
	for _, b := range []struct{ dir, pkg, out string }{
		{"..", "./cmd/gantline", "gantline"},
		{"testdata/echomcp", ".", "echomcp"},
	} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(bin, b.out), b.pkg)
		cmd.Dir = b.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", b.out, err, out)
		}
	}
	return bin
}

// settingsModel is the "model" key of ~/.claude/settings.json, or "".
// A model switch must never change it.
func settingsModel(t *testing.T) string {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var v struct {
		Model string `json:"model"`
	}
	json.Unmarshal(data, &v)
	return v.Model
}

func selfControl(t *testing.T, bin string) {
	home, _ := os.UserHomeDir()
	project, err := os.MkdirTemp(filepath.Join(home, ".cache"), "e2e-gl-") // not "gantline-…": some hooks match sessions by a cwd-name prefix
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("project %s", project)
	writeProject(t, project, bin, steps, nil)

	s := tmux.Server{Socket: fmt.Sprintf("gl-e2e-%d", time.Now().UnixNano())}
	env := []string{"env", "PATH=" + bin + ":" + os.Getenv("PATH"), "GANTLINE_TMUX_SOCKET=" + s.Socket}
	launch := strings.Join(append(env, "claude", "--model", "haiku"), " ")
	if _, err := s.Run(newSession(project, launch)...); err != nil {
		t.Fatal(err)
	}
	servers.Lock()
	servers.list = append(servers.list, s)
	servers.Unlock()
	sock, _ := s.Run("display-message", "-p", "-t", "%0", "#{socket_path}")
	key := journal.Key(strings.TrimSpace(sock), "%0")
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("journal and queue kept in %s, project in %s", journal.PaneDir(key), project)
			saveFailureCapture(t, s)
		}
		endSession(s, key, project, !t.Failed())
	})

	if err := acceptTrust(s); err != nil {
		t.Fatal(err)
	}
	gl := func(args ...string) string {
		cmd := exec.Command(filepath.Join(bin, "gantline"), args...)
		cmd.Env = append(os.Environ(), "GANTLINE_TMUX_SOCKET="+s.Socket)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("gantline %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if live() {
		if _, err := s.Run("resize-window", "-t", "%0", "-x", "56", "-y", "16"); err != nil {
			t.Fatal(err)
		}
	}
	if err := acceptTrust(s); err != nil {
		t.Fatal(err)
	}
	runStart := time.Now()
	defaultModel := settingsModel(t)
	sayFirst(t, bin, s.Socket, prompt)

	done := filepath.Join(project, "done.txt")
	deadline := time.Now().Add(15 * time.Minute)
	for {
		if _, err := os.Stat(done); err == nil {
			break
		}
		if time.Now().After(deadline) {
			items, _ := queue.Items(key)
			c, _ := s.Capture("%0")
			t.Fatalf("no done.txt after 15m; queue %+v\nscreen:\n%s", items, c)
		}
		time.Sleep(5 * time.Second)
	}
	checkRun(t, project, key)
	// No keystroke delivery or relaunch began while the journal says the
	// session was busy (gantline audit exits 1 and lists any that did).
	t.Log(strings.TrimSpace(gl("audit", "--e2e", "--since", time.Since(runStart).Round(time.Second).String())))
	if now := settingsModel(t); now != defaultModel {
		t.Errorf("the settings default model changed from %q to %q", defaultModel, now)
	}
}

// writeProject writes the scratch project: hooks, statusline, the echo MCP
// server and steps/N.txt. extra adds settings keys (a scenario's
// permissions); a "permissions" key replaces the default allow list.
func writeProject(t *testing.T, project, bin string, steps []string, extra map[string]any) {
	g := filepath.Join(bin, "gantline")
	hooks := map[string]any{}
	for _, ev := range []string{"Notification", "PermissionRequest", "PostCompact", "PostModelSwitch", "PostToolUse",
		"PreCompact", "SessionEnd", "SessionStart", "Stop", "StopFailure", "UserPromptSubmit"} {
		h := map[string]any{"type": "command", "command": g + " hook", "timeout": 5}
		// Their stdout reaches the model only when the hook is synchronous.
		if ev != "UserPromptSubmit" && ev != "SessionStart" {
			h["async"] = true
		}
		hooks[ev] = []any{map[string]any{"matcher": "", "hooks": []any{h}}}
	}
	if os.Getenv("GANTLINE_E2E_BLOCKSTOP") == "1" {
		src, _ := filepath.Abs("testdata/blockstop/block-once.sh")
		data, _ := os.ReadFile(src)
		os.WriteFile(filepath.Join(project, "block-once.sh"), data, 0o700)
		stop := hooks["Stop"].([]any)
		hooks["Stop"] = append(stop, map[string]any{"matcher": "", "hooks": []any{
			map[string]any{"type": "command", "command": filepath.Join(project, "block-once.sh")}}})
	}
	settings := map[string]any{
		"hooks":                      hooks,
		"statusLine":                 map[string]any{"type": "command", "command": g + " statusline"},
		"enableAllProjectMcpServers": true,
		"permissions":                map[string]any{"allow": []string{"Bash(gantline:*)", "Bash(touch:*)", "Read"}},
	}
	for k, v := range extra {
		settings[k] = v
	}
	mcp := map[string]any{"mcpServers": map[string]any{"echomcp": map[string]any{"command": filepath.Join(bin, "echomcp")}}}
	os.MkdirAll(filepath.Join(project, ".claude"), 0o700)
	os.MkdirAll(filepath.Join(project, "steps"), 0o700)
	writeJSON(t, filepath.Join(project, ".claude", "settings.local.json"), settings)
	writeJSON(t, filepath.Join(project, ".mcp.json"), mcp)
	for i, s := range steps {
		if i == 0 && live() {
			// A background task that finishes mid-run starts a turn of its
			// own (a task notification), as on a busy pane.
			s = "First start `sleep 30` with the Bash tool's run_in_background option, and don't wait for it. " + s
		}
		if i == 1 && live() {
			// In a 16-row pane /context is taller than the screen, and the
			// alternate screen keeps no scrollback, so by this turn the panel
			// is gone: read the copy gantline took off the screen when it
			// finished instead.
			s = strings.Replace(s, "gantline screen --scrollback 300 --find '/[0-9.]+[km] tokens' > panel.txt", "gantline q --show q1 --screen > panel.txt", 1)
		}
		os.WriteFile(filepath.Join(project, "steps", fmt.Sprintf("%d.txt", i+1)), []byte(s+"\n"), 0o600)
	}
	os.WriteFile(filepath.Join(project, "compact.txt"), []byte(compactCmd), 0o600)
}

var ctxLine = regexp.MustCompile(`[0-9.]+[km]?/[0-9.]+[km] tokens \(`)

// live is the GANTLINE_E2E_LIVE=1 variant: a 56x16 pane like one slot of a
// multi-pane layout, which cuts the mode line, and a background task
// finishing mid-run.
func live() bool { return os.Getenv("GANTLINE_E2E_LIVE") == "1" }

func writeJSON(t *testing.T, path string, v any) {
	data, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// errStall marks a launch that never reached an empty input box: a host
// startup stall (README "Limits"), not a gantline failure.
var errStall = errors.New("session never reached an empty input box")

// acceptTrust answers the folder trust dialog of this throwaway project,
// then waits for the input box.
func acceptTrust(s tmux.Server) error {
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if st, err := s.Run("display-message", "-p", "-t", "%0", "#{pane_dead} #{pane_dead_status}"); err == nil && strings.HasPrefix(st, "1 ") {
			c, _ := s.Capture("%0")
			return fmt.Errorf("%w: claude exited at startup with status %s; its last screen:\n%s", errStall, strings.TrimSpace(strings.TrimPrefix(st, "1 ")), c)
		}
		c, _ := s.Capture("%0")
		l := screen.Cut(c)
		if screen.Dialog(l) == "trust_folder" {
			s.Run("send-keys", "-t", "%0", "Down")
			time.Sleep(300 * time.Millisecond)
			s.Run("send-keys", "-t", "%0", "Enter")
			continue
		}
		if in, ok := screen.Input(c); ok && in == "" && !screen.Busy(c) {
			return nil
		}
	}
	c, _ := s.Capture("%0")
	// Startups that draw nothing for 90s have fired no SessionStart either.
	// The process tree shows whether a SessionStart hook is still running.
	st, err := s.Run("display-message", "-p", "-t", "%0", "pid=#{pane_pid} dead=#{pane_dead} status=#{pane_dead_status} cmd=#{pane_current_command}")
	if err != nil {
		// The pane, or the whole tmux server, is gone: claude exited at
		// startup and took the server with it.
		return fmt.Errorf("%w: the pane is gone (%v)", errStall, err)
	}
	pid := ""
	if f := strings.Fields(st); len(f) > 0 {
		pid = strings.TrimPrefix(f[0], "pid=")
	}
	tree, err := exec.Command("pstree", "-pal", pid).CombinedOutput()
	return fmt.Errorf("%w:\n%s\npane: %s\nprocesses (%v):\n%s", errStall, c, strings.TrimSpace(st), err, tree)
}

// checkRun asserts the outcome from the queue, the journal and the files
// the session wrote.
func checkRun(t *testing.T, project, key string) {
	items, err := queue.Items(key)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/context", "/compact", "/mcp reconnect echomcp", "relaunch --model sonnet"}
	if len(items) != len(want) {
		t.Fatalf("queue has %d items, want %d: %+v", len(items), len(want), items)
	}
	for i, it := range items {
		wantThen := queue.Done
		if it.Then == "" {
			wantThen = ""
		}
		if want[i] == "/compact" && len(it.Cmd) < 900 {
			t.Errorf("the queued /compact is %d bytes; the test needs one over 900", len(it.Cmd))
		}
		if !strings.HasPrefix(it.Cmd, want[i]) || it.State != queue.Done || it.ThenState != wantThen {
			t.Errorf("item %d = %s %q state %s (%s) follow-up %q", i, it.ID, it.Cmd, it.State, it.Detail, it.ThenState)
		}
	}

	evs, _ := journal.Events(key)
	var pre, post, compactStart, resumed time.Time
	var sids = map[string]bool{}
	for _, e := range evs {
		switch {
		case e.Ev == "PreCompact" && pre.IsZero():
			pre = e.Time()
		case e.Ev == "SessionStart" && e.Source == "compact":
			compactStart = e.Time()
		case e.Ev == "PostCompact" && post.IsZero():
			post = e.Time()
		case e.Ev == "SessionStart" && e.Source == "resume":
			resumed = e.Time()
		}
		if e.Sid != "" {
			sids[e.Sid] = true
		}
	}
	if pre.IsZero() || compactStart.Before(pre) || post.Before(compactStart) {
		t.Errorf("compaction order: PreCompact %v, SessionStart(compact) %v, PostCompact %v", pre, compactStart, post)
	}
	// The relaunch resumed the same session: one session id throughout.
	if resumed.IsZero() || len(sids) != 1 {
		t.Errorf("relaunch: resume SessionStart at %v, session ids %v", resumed, sids)
	}
	var st5 struct {
		Model string `json:"model"`
	}
	if fi, err := os.Stat(filepath.Join(project, "step5.json")); err != nil || !fi.ModTime().After(resumed) {
		t.Errorf("step5.json missing or written before the relaunch: %v", err)
	}
	readJSON(t, filepath.Join(project, "step5.json"), &st5)
	if !strings.Contains(st5.Model, "sonnet") {
		t.Errorf("step5.json model = %q, want sonnet after the relaunch", st5.Model)
	}

	// The session carried on after its own compaction with nobody typing:
	// step3.json was written after PostCompact and counts the compaction.
	var st3 struct {
		Compactions int    `json:"compactions"`
		Activity    string `json:"activity"`
	}
	if fi, err := os.Stat(filepath.Join(project, "step3.json")); err != nil || !fi.ModTime().After(post) {
		t.Errorf("step3.json missing or written before PostCompact: %v", err)
	}
	readJSON(t, filepath.Join(project, "step3.json"), &st3)
	if st3.Compactions < 1 {
		t.Errorf("step3.json compactions = %d, want >= 1", st3.Compactions)
	}
	if step1, _ := os.ReadFile(filepath.Join(project, "step1.txt")); !strings.Contains(string(step1), "%0") {
		t.Errorf("step1.txt doesn't hold a status line: %q", step1)
	}
	// /context's full panel, read back from the transcript (a 16-row pane
	// scrolls it off the screen), and the mode line read from the screen
	// (this project's statusline prints nothing, so it has no row).
	if scr, _ := os.ReadFile(filepath.Join(project, "screen.txt")); !strings.Contains(string(scr), "mode on") {
		t.Errorf("screen.txt doesn't hold the mode line: %q", clip(string(scr)))
	}
	// The same panel read off the pane: by the session itself in the
	// full-size pane, from gantline's copy taken as /context finished in
	// the 16-row one (only the panel's last rows fit there).
	panel, _ := os.ReadFile(filepath.Join(project, "panel.txt"))
	switch {
	case live() && strings.Contains(string(panel), "Free space"):
		t.Logf("screen copy holds the visible end of the /context panel")
	case !live() && ctxLine.Match(panel):
		t.Logf("screen read found the /context panel")
	default:
		t.Errorf("panel.txt doesn't hold the /context panel read off the screen: %q", clip(string(panel)))
	}
	if ctx, _ := os.ReadFile(filepath.Join(project, "ctx.txt")); !ctxLine.Match(ctx) {
		t.Errorf("ctx.txt doesn't hold the /context panel: %q", clip(string(ctx)))
	}
	var st4 struct {
		Activity string `json:"activity"`
	}
	readJSON(t, filepath.Join(project, "step4.json"), &st4)
	if st4.Activity != "working" {
		t.Errorf("step4.json activity = %q, want working (it was written mid-turn)", st4.Activity)
	}

	// Law 10: the outcomes reached the model through the hook footer.
	st, _, _ := journal.Load(key)
	if tr, err := os.ReadFile(st.Transcript); err != nil || !bytes.Contains(tr, []byte("gantline: q1 /context done")) {
		t.Errorf("no footer for q1 in the transcript %s (%v)", st.Transcript, err)
	}
	if os.Getenv("GANTLINE_E2E_BLOCKSTOP") == "1" {
		if _, err := os.Stat(filepath.Join(project, ".blocked")); err != nil {
			t.Error("blockstop variant: the blocking Stop hook never ran")
		}
	}
}

func readJSON(t *testing.T, path string, v any) {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("%s: %v", filepath.Base(path), err)
		return
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Errorf("%s: %v", filepath.Base(path), err)
	}
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// saveFailureCapture keeps the pane's last screen, with colour, in the
// local corpus (never git), so an unexplained screen can be fingerprinted.
// endSession kills s and, when remove is set, deletes the pane's journal
// and the scratch project once claude has exited. Killing the server fires
// SessionEnd, whose hook writes the journal again, so deleting first leaves
// a one-line dir behind.
func endSession(s tmux.Server, key, project string, remove bool) {
	pid := 0
	if out, err := s.Run("display-message", "-p", "-t", "%0", "#{pane_pid}"); err == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(out))
	}
	s.Run("kill-server")
	if !remove {
		return
	}
	for deadline := time.Now().Add(10 * time.Second); pid > 0 && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			break
		}
	}
	time.Sleep(time.Second) // an async hook can outlive claude by a moment
	os.RemoveAll(journal.PaneDir(key))
	removeProject(project)
}

// newSession is the tmux command line that starts a test session. The
// server keeps a pane whose process exited, so a claude that quits at
// startup leaves its last screen for acceptTrust to report.
func newSession(project, command string) []string {
	return []string{"start-server", ";", "set-option", "-g", "remain-on-exit", "on", ";",
		"new-session", "-d", "-x", "200", "-y", "50", "-c", project, command}
}

// sayFirst sends a session's first prompt. A freshly drawn input box can
// drop every key while the session is still starting; say reports that
// as ErrDropped with the box left empty, so typing again is safe.
func sayFirst(t *testing.T, bin, socket, prompt string) {
	t.Helper()
	for try := 1; ; try++ {
		cmd := exec.Command(filepath.Join(bin, "gantline"), "say", "%0", prompt)
		cmd.Env = append(os.Environ(), "GANTLINE_TMUX_SOCKET="+socket)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return
		}
		if try == 4 || !strings.Contains(string(out), "none of it reached the box") {
			t.Fatalf("gantline say (try %d): %v\n%s", try, err, out)
		}
		t.Logf("first prompt dropped by a starting session (try %d); typing again in 5s", try)
		time.Sleep(5 * time.Second)
	}
}

// removeProject deletes a scratch project made by os.MkdirTemp(~/.cache,
// "e2e-gl-"), and refuses any other path.
func removeProject(project string) {
	home, err := os.UserHomeDir()
	if err != nil || project == "" {
		return
	}
	clean := filepath.Clean(project)
	if filepath.Dir(clean) != filepath.Join(home, ".cache") || !strings.HasPrefix(filepath.Base(clean), "e2e-gl-") {
		fmt.Fprintf(os.Stderr, "e2e: not deleting %q: not a scratch project\n", project)
		return
	}
	os.RemoveAll(clean)
}

func saveFailureCapture(t *testing.T, s tmux.Server) {
	c, err := s.CaptureScroll("%0", 0, true)
	if err != nil {
		return
	}
	out, _ := exec.Command("claude", "--version").Output()
	version := strings.Fields(string(out) + " unknown")[0]
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "state", "gantline", "corpus", version, "e2e")
	os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, fmt.Sprintf("fail-%d.txt", time.Now().Unix()))
	if os.WriteFile(path, []byte(c), 0o600) == nil {
		t.Logf("screen saved to %s", path)
	}
}

// servers are the tmux servers runs started, for the signal and deadline
// paths, which skip t.Cleanup.
var servers struct {
	sync.Mutex
	list []tmux.Server
}

func killServers() {
	servers.Lock()
	defer servers.Unlock()
	for _, s := range servers.list {
		s.Run("kill-server")
	}
}

// removeProject deletes only a direct ~/.cache/e2e-gl-* dir.
func TestRemoveProjectGuard(t *testing.T) {
	home, _ := os.UserHomeDir()
	cache := filepath.Join(home, ".cache")
	ok, err := os.MkdirTemp(cache, "e2e-gl-")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := os.MkdirTemp(cache, "not-e2e-")
	nested, _ := os.MkdirTemp(t.TempDir(), "e2e-gl-")
	defer os.RemoveAll(other)
	for _, p := range []string{ok, other, nested} {
		os.WriteFile(filepath.Join(p, "marker"), nil, 0o600)
	}
	for _, p := range []string{ok, other, nested, cache, ok + "/..", ""} {
		removeProject(p)
	}
	if _, err := os.Stat(ok); !os.IsNotExist(err) {
		t.Errorf("%s survived", ok)
	}
	for _, p := range []string{other, nested} { // other surviving means ~/.cache did too
		if _, err := os.Stat(filepath.Join(p, "marker")); err != nil {
			t.Errorf("%s was deleted: %v", p, err)
		}
	}
}
