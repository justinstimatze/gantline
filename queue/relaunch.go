package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/justinstimatze/gantline/internal/audit"
	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/relaunch"
	"github.com/justinstimatze/gantline/screen"
)

// comeBack bounds how long a relaunched session gets to report in.
const comeBack = 90 * time.Second

// relaunchSession restarts the pane's claude with --resume and --model, at
// an idle screen with an empty box (a person's draft would be lost), and
// checks the session came back as itself on the asked-for model.
func (r *runner) relaunchSession(it Item, deadline time.Time) (state, detail string) {
	for {
		if err := r.waitIdle(deadline); err != nil {
			return Expired, err.Error()
		}
		c, err := r.s.Capture(r.pane)
		if err != nil {
			return Failed, err.Error()
		}
		if draft, ok := screen.Input(c); ok && draft == "" && !screen.Busy(c) {
			break
		}
		if time.Now().After(deadline) {
			return Expired, "a draft stayed in the input box"
		}
		time.Sleep(r.o.Poll)
	}
	release, err := journal.LockWrites(r.key, 10*time.Second)
	if err != nil {
		return Failed, err.Error()
	}
	defer release()

	out, err := r.s.Run("display-message", "-p", "-t", r.pane, "#{pane_pid}")
	if err != nil {
		return Failed, err.Error()
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(out))
	find := r.o.FindSession
	if find == nil {
		find = relaunch.Find
	}
	sess, err := find(pid)
	if err != nil {
		return Failed, err.Error()
	}
	// Claude Code writes a session's transcript at its first message; before
	// that, --resume finds nothing, prints "No conversation found" and exits.
	if st, _, err := journal.Load(r.key); err == nil && st.Transcript != "" && strings.Contains(st.Transcript, sess.ID) {
		if _, err := os.Stat(st.Transcript); os.IsNotExist(err) {
			return Rejected, "the session has no messages yet, so there is nothing to resume; nothing was restarted"
		}
	}
	argv, err := relaunch.Argv(sess.Argv, sess.ID, it.Model)
	if err != nil {
		return Failed, err.Error()
	}
	before := settingsModel()
	self, err := os.Executable()
	if err != nil {
		return Failed, err.Error()
	}
	// The environment goes through a 0600 file `gantline launch` reads and
	// deletes, never onto a command line other users can see in ps.
	lf, err := relaunch.WriteLaunch(journal.PaneDir(r.key), relaunch.Launch{
		Dir: sess.Dir, Env: sess.Env, Argv: argv, Shell: sess.PID != pid})
	if err != nil {
		return Failed, err.Error()
	}
	shown := strings.Join(relaunch.Redact(argv), " ")
	start := time.Now()
	audit.Write(audit.Rec{Kind: "relaunch", Pane: r.pane, Extra: map[string]any{"key": r.key, "id": it.ID, "start_ns": start.UnixNano()}})
	r.o.Log("%s: relaunching session %s: %s", it.ID, sess.ID, shown)
	if _, err := r.s.Run("respawn-pane", "-k", "-t", r.pane, "-c", sess.Dir, self, "launch", lf); err != nil {
		os.Remove(lf)
		return Failed, "respawn-pane: " + err.Error()
	}

	// Back means a resume SessionStart for the same session id, then a
	// statusline snapshot from it naming the model.
	resumed := false
	for limit := start.Add(comeBack); time.Now().Before(limit); time.Sleep(r.o.Poll) {
		if !resumed {
			evs, _ := journal.Events(r.key)
			for _, e := range journal.After(evs, start) {
				if e.Ev == "SessionStart" && e.Source == "resume" && e.Sid == sess.ID {
					resumed = true
				}
			}
			continue
		}
		st, err := journal.ReadStatus(r.key)
		if err != nil || st.T < start.UnixNano() || st.SessionID != sess.ID || st.ModelID == "" {
			continue
		}
		note := ""
		if after := settingsModel(); after != before {
			note = fmt.Sprintf("; the settings default model changed from %q to %q", before, after)
		}
		if !modelMatches(st.ModelID, it.Model) {
			return Rejected, fmt.Sprintf("session %s came back on %s, not %s%s", sess.ID, st.ModelID, it.Model, note)
		}
		return Done, fmt.Sprintf("session %s resumed on %s%s", sess.ID, st.ModelID, note)
	}
	dead, _ := r.s.Run("display-message", "-p", "-t", r.pane, "#{pane_dead}")
	what := "no resume SessionStart"
	if resumed {
		what = "resumed, but no statusline snapshot named a model"
	}
	if strings.TrimSpace(dead) == "1" {
		what = "the pane's process died"
	}
	return Failed, fmt.Sprintf("%s within %s; command: %s", what, comeBack, shown)
}

// modelMatches reports whether the running model id satisfies the asked-for
// name: the same id, or an alias ("sonnet", "haiku") it contains.
func modelMatches(id, asked string) bool {
	id, asked = strings.ToLower(id), strings.ToLower(asked)
	return id == asked || strings.HasPrefix(id, asked) || strings.Contains(id, "-"+asked+"-") || strings.HasPrefix(id, "claude-"+asked)
}

// settingsModel is the "model" key of ~/.claude/settings.json, or "".
func settingsModel() string {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return ""
	}
	var s struct {
		Model string `json:"model"`
	}
	json.Unmarshal(data, &s)
	return s.Model
}
