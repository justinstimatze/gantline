//go:build realclaude

package e2e

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/justinstimatze/gantline/internal/proc"
	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/queue"
	"github.com/justinstimatze/gantline/screen"
	"github.com/justinstimatze/gantline/tmux"
)

// A scenario drives one fresh Haiku session through one self-control path
// that TestSelfControl doesn't cover, acting a person's part (a draft, an
// Esc, a permission answer) with tmux keys from outside the session.
type scenario struct {
	name  string
	steps []string
	extra map[string]any // settings keys, such as a scenario's permissions
	drive func(t *testing.T, x *sess)
}

// sess is one running scenario session.
type sess struct {
	s       tmux.Server
	key     string
	project string
	bin     string
	start   time.Time
}

const next = "E2E: read steps/2.txt and do exactly what it says."

var finish = "Run `gantline status --json > after.json`, then run `touch done.txt`. Then reply only: done"

var scenarios = []scenario{
	{
		name:  "clear",
		steps: []string{"Run `gantline q --then '" + next + "' /clear`. Then end your turn by replying only: ok", finish},
		drive: func(t *testing.T, x *sess) {
			x.waitFile(t, "done.txt", 4*time.Minute)
			it := x.onlyItem(t)
			if !strings.HasPrefix(it.Cmd, "/clear") || it.State != queue.Done || it.ThenState != queue.Done {
				t.Errorf("item %+v", it)
			}
			clr := x.firstEvent(func(e journal.Event) bool { return e.Ev == "SessionStart" && e.Source == "clear" })
			if clr.IsZero() {
				t.Fatal("no SessionStart with source clear")
			}
			if sids := x.sessionIDs(); len(sids) != 2 {
				t.Errorf("session ids %v, want the old one and the cleared one", sids)
			}
			x.writtenAfter(t, "after.json", clr)
		},
	},
	{
		name:  "restart",
		steps: []string{"Run `gantline restart --then '" + next + "'`. Then end your turn by replying only: ok", finish},
		drive: func(t *testing.T, x *sess) {
			x.waitFile(t, "done.txt", 4*time.Minute)
			it := x.onlyItem(t)
			if !strings.Contains(it.Model, "haiku") || it.State != queue.Done || it.ThenState != queue.Done {
				t.Errorf("item %+v, want a done relaunch on the same haiku model", it)
			}
			res := x.firstEvent(func(e journal.Event) bool { return e.Ev == "SessionStart" && e.Source == "resume" })
			if res.IsZero() {
				t.Fatal("no SessionStart with source resume")
			}
			if sids := x.sessionIDs(); len(sids) != 1 {
				t.Errorf("session ids %v, want one: the restart resumes the same conversation", sids)
			}
			x.writtenAfter(t, "after.json", res)
			if after, _ := os.ReadFile(filepath.Join(x.project, "after.json")); !strings.Contains(string(after), "haiku") {
				t.Errorf("after.json doesn't show the haiku model: %s", clip(string(after)))
			}
		},
	},
	{
		// The runner never types over a person's draft: the draft goes in
		// while the turn runs, the item waits past the turn end, and runs
		// once the draft is gone.
		name: "draft",
		steps: []string{"Run `gantline q --then '" + next + "' /context`. Then run `sleep 15`. Then end your turn by replying only: ok",
			finish},
		extra: map[string]any{"permissions": map[string]any{"allow": []string{"Bash(gantline:*)", "Bash(touch:*)", "Bash(sleep:*)", "Read"}}},
		drive: func(t *testing.T, x *sess) {
			it := x.waitItem(t, 3*time.Minute)
			const draft = "a person is typing this"
			if _, err := x.s.Run("send-keys", "-t", "%0", "-l", draft); err != nil {
				t.Fatal(err)
			}
			x.waitEvent(t, "Stop", it.EnqTime(), 3*time.Minute)
			time.Sleep(20 * time.Second)
			it = x.onlyItem(t)
			if it.Typed != 0 || queue.Final(it.State) {
				t.Fatalf("with a draft in the box the item went ahead: %+v", it)
			}
			c, _ := x.s.Capture("%0")
			if in, _ := screen.Input(c); !screen.SameText(in, draft) {
				t.Fatalf("box holds %q, want the draft untouched", in)
			}
			for range draft {
				x.s.Run("send-keys", "-t", "%0", "BSpace")
			}
			x.waitFile(t, "done.txt", 4*time.Minute)
			if it = x.onlyItem(t); it.State != queue.Done || it.ThenState != queue.Done {
				t.Errorf("after the draft was cleared: %+v", it)
			}
		},
	},
	{
		// An open permission dialog holds the item until a person answers.
		name: "permission",
		steps: []string{"Run `gantline q --then '" + next + "' /context`. Then run `mkdir perm-check` with the Bash tool. Then end your turn by replying only: ok",
			finish},
		extra: map[string]any{"permissions": map[string]any{
			"defaultMode": "default",
			"allow":       []string{"Bash(gantline:*)", "Bash(touch:*)", "Read"},
			"ask":         []string{"Bash(mkdir:*)"},
		}},
		drive: func(t *testing.T, x *sess) {
			it := x.waitItem(t, 3*time.Minute)
			x.waitEvent(t, "PermissionRequest", it.EnqTime(), 3*time.Minute)
			time.Sleep(20 * time.Second)
			if it = x.onlyItem(t); it.Typed != 0 || queue.Final(it.State) {
				t.Fatalf("with a permission dialog open the item went ahead: %+v", it)
			}
			c, _ := x.s.Capture("%0")
			if l := screen.Cut(c); l.Dialog.Empty() {
				t.Fatalf("no dialog on screen while the permission request is open:\n%s", c)
			}
			// A person picks the first option, Yes, here and on any later
			// prompt: the host's global Stop hooks can start a turn of their
			// own that asks again, and without auto mode nothing else answers.
			x.s.Run("send-keys", "-t", "%0", "Enter")
			for deadline := time.Now().Add(4 * time.Minute); ; time.Sleep(2 * time.Second) {
				if _, err := os.Stat(filepath.Join(x.project, "done.txt")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					c, _ := x.s.Capture("%0")
					t.Fatalf("no done.txt within 4m; queue %+v\nscreen:\n%s", x.onlyItem(t), c)
				}
				if c, _ := x.s.Capture("%0"); screen.Cut(c).Box.Empty() && !screen.Cut(c).Dialog.Empty() {
					x.s.Run("send-keys", "-t", "%0", "Enter")
				}
			}
			if _, err := os.Stat(filepath.Join(x.project, "perm-check")); err != nil {
				t.Errorf("the approved mkdir didn't run: %v", err)
			}
			if it = x.onlyItem(t); it.State != queue.Done || it.ThenState != queue.Done {
				t.Errorf("after the answer: %+v", it)
			}
		},
	},
	{
		// An Esc interrupt fires no Stop hook, so the item keeps waiting
		// for a turn end; the next turn's Stop releases it.
		name: "esc",
		steps: []string{"Run `gantline q --then '" + next + "' /context`. Then run `sleep 40`. Then end your turn by replying only: ok",
			finish},
		extra: map[string]any{"permissions": map[string]any{"allow": []string{"Bash(gantline:*)", "Bash(touch:*)", "Bash(sleep:*)", "Read"}}},
		drive: func(t *testing.T, x *sess) {
			it := x.waitItem(t, 3*time.Minute)
			time.Sleep(6 * time.Second) // into the sleep
			x.s.Run("send-keys", "-t", "%0", "Escape")
			time.Sleep(25 * time.Second)
			if stop := x.eventAfter("Stop", it.EnqTime()); !stop.IsZero() {
				t.Logf("a Stop fired after the Esc at %v: this Claude Code version reports interrupts", stop)
			}
			if it = x.onlyItem(t); it.Typed != 0 || queue.Final(it.State) {
				t.Fatalf("the item went ahead after an interrupt with no turn end: %+v", it)
			}
			x.gl(t, "say", "%0", "E2E: the last turn was interrupted on purpose. Reply only: ok")
			x.waitFile(t, "done.txt", 5*time.Minute)
			if it = x.onlyItem(t); it.State != queue.Done || it.ThenState != queue.Done {
				t.Errorf("after the next turn: %+v", it)
			}
		},
	},
	{
		// Kill the runner after it types /compact: its item is left
		// running, and the next runner must mark it unconfirmed without
		// typing it again.
		name:  "killed-runner",
		steps: []string{"Run `gantline q --then '" + next + "' '/compact keep: this is the gantline e2e test'`. Then end your turn by replying only: ok", finish},
		drive: func(t *testing.T, x *sess) {
			var it queue.Item
			for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(50 * time.Millisecond) {
				if items, _ := queue.Items(x.key); len(items) > 0 && items[0].Typed != 0 {
					it = items[0]
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the /compact was never typed")
				}
			}
			syscall.Kill(x.runnerPID(t), syscall.SIGKILL)
			if it = x.onlyItem(t); queue.Final(it.State) {
				t.Skipf("inconclusive: q1 was already %s when the runner was killed", it.State)
			}
			x.waitEvent(t, "PostCompact", it.EnqTime(), 3*time.Minute)
			x.gl(t, "q", "--pane", "%0", "/context")
			for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(time.Second) {
				items, _ := queue.Items(x.key)
				if len(items) == 2 && queue.Final(items[1].State) {
					if items[0].State != queue.Unconfirmed || !strings.Contains(items[0].Detail, "runner stopped") || items[0].ThenState != "" {
						t.Errorf("q1 = %+v, want unconfirmed as orphaned with no follow-up", items[0])
					}
					if items[1].State != queue.Done {
						t.Errorf("q2 = %+v", items[1])
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("q2 never finished: %+v", items)
				}
			}
			// Each hook fires twice here, from this host's own gantline
			// install and from the project's settings: count PreCompacts
			// more than a second apart.
			n, last := 0, time.Time{}
			for _, e := range x.events() {
				if e.Ev == "PreCompact" && e.Time().Sub(last) > time.Second {
					n, last = n+1, e.Time()
				}
			}
			if n != 1 {
				t.Errorf("%d compactions, want 1: the orphaned /compact was typed again", n)
			}
		},
	},
}

// TestScenarios runs each scenario in its own session, one at a time.
// GANTLINE_E2E_SCENARIOS=clear,esc picks some. A launch that stalls is
// retried once, then the scenario is skipped as a host stall.
func TestScenarios(t *testing.T) {
	pick := map[string]bool{}
	for _, n := range strings.Split(os.Getenv("GANTLINE_E2E_SCENARIOS"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			pick[n] = true
		}
	}
	bin := buildBinaries(t)
	guardServers(t)
	var passed, failed, stalled []string
	for _, sc := range scenarios {
		if len(pick) > 0 && !pick[sc.name] {
			continue
		}
		stall := false
		ok := t.Run(sc.name, func(t *testing.T) {
			start := time.Now()
			x, err := launch(t, bin, sc)
			if errors.Is(err, errStall) {
				t.Logf("launch stalled; retrying once: %v", firstLine(err))
				x, err = launch(t, bin, sc)
			}
			if errors.Is(err, errStall) {
				stall = true
				t.Skipf("host stall on both launches: %v", firstLine(err))
			}
			if err != nil {
				t.Fatal(err)
			}
			sc.drive(t, x)
			t.Logf("audit: %s", strings.TrimSpace(x.glOut("audit", "--e2e", "--since", time.Since(x.start).Round(time.Second).String())))
			t.Logf("took %v", time.Since(start).Round(time.Second))
		})
		switch {
		case stall:
			stalled = append(stalled, sc.name)
		case ok:
			passed = append(passed, sc.name)
		default:
			failed = append(failed, sc.name)
		}
	}
	t.Logf("passed %v; failed %v; host stalls %v", passed, failed, stalled)
}

// launch starts a scenario's session and sends its first prompt.
func launch(t *testing.T, bin string, sc scenario) (*sess, error) {
	home, _ := os.UserHomeDir()
	project, err := os.MkdirTemp(filepath.Join(home, ".cache"), "e2e-gl-")
	if err != nil {
		return nil, err
	}
	t.Logf("project %s", project)
	writeProject(t, project, bin, sc.steps, sc.extra)
	s := tmux.Server{Socket: fmt.Sprintf("gl-e2e-%d", time.Now().UnixNano())}
	env := []string{"env", "PATH=" + bin + ":" + os.Getenv("PATH"), "GANTLINE_TMUX_SOCKET=" + s.Socket}
	if _, err := s.Run(newSession(project, strings.Join(append(env, "claude", "--model", "haiku"), " "))...); err != nil {
		return nil, err
	}
	servers.Lock()
	servers.list = append(servers.list, s)
	servers.Unlock()
	sock, _ := s.Run("display-message", "-p", "-t", "%0", "#{socket_path}")
	x := &sess{s: s, key: journal.Key(strings.TrimSpace(sock), "%0"), project: project, bin: bin}
	if err := acceptTrust(s); err != nil {
		endSession(s, x.key, "", true) // a host stall keeps its project
		return nil, err
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("journal and queue kept in %s, project in %s", journal.PaneDir(x.key), x.project)
			saveFailureCapture(t, s)
		}
		endSession(s, x.key, x.project, !t.Failed())
	})
	x.start = time.Now()
	sayFirst(t, bin, s.Socket, prompt)
	return x, nil
}

func (x *sess) glOut(args ...string) string {
	cmd := exec.Command(filepath.Join(x.bin, "gantline"), args...)
	cmd.Env = append(os.Environ(), "GANTLINE_TMUX_SOCKET="+x.s.Socket)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("%s (%v)", out, err)
	}
	return string(out)
}

func (x *sess) gl(t *testing.T, args ...string) {
	cmd := exec.Command(filepath.Join(x.bin, "gantline"), args...)
	cmd.Env = append(os.Environ(), "GANTLINE_TMUX_SOCKET="+x.s.Socket)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gantline %v: %v\n%s", args, err, out)
	}
}

func (x *sess) events() []journal.Event {
	evs, _ := journal.Events(x.key)
	return evs
}

func (x *sess) firstEvent(match func(journal.Event) bool) time.Time {
	for _, e := range x.events() {
		if match(e) {
			return e.Time()
		}
	}
	return time.Time{}
}

func (x *sess) eventAfter(ev string, after time.Time) time.Time {
	return x.firstEvent(func(e journal.Event) bool { return e.Ev == ev && e.Time().After(after) })
}

func (x *sess) waitEvent(t *testing.T, ev string, after time.Time, d time.Duration) time.Time {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(time.Second) {
		if at := x.eventAfter(ev, after); !at.IsZero() {
			return at
		}
	}
	c, _ := x.s.Capture("%0")
	t.Fatalf("no %s after %v within %v; screen:\n%s", ev, after.Format(time.TimeOnly), d, c)
	return time.Time{}
}

func (x *sess) sessionIDs() map[string]bool {
	sids := map[string]bool{}
	for _, e := range x.events() {
		if e.Sid != "" {
			sids[e.Sid] = true
		}
	}
	return sids
}

func (x *sess) waitItem(t *testing.T, d time.Duration) queue.Item {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if items, _ := queue.Items(x.key); len(items) > 0 {
			return items[0]
		}
	}
	c, _ := x.s.Capture("%0")
	t.Fatalf("nothing queued within %v; screen:\n%s", d, c)
	return queue.Item{}
}

func (x *sess) onlyItem(t *testing.T) queue.Item {
	items, err := queue.Items(x.key)
	if err != nil || len(items) != 1 {
		t.Fatalf("queue %+v (%v), want one item", items, err)
	}
	return items[0]
}

func (x *sess) waitFile(t *testing.T, name string, d time.Duration) {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if _, err := os.Stat(filepath.Join(x.project, name)); err == nil {
			return
		}
	}
	items, _ := queue.Items(x.key)
	c, _ := x.s.Capture("%0")
	t.Fatalf("no %s within %v; queue %+v\nscreen:\n%s", name, d, items, c)
}

func (x *sess) writtenAfter(t *testing.T, name string, at time.Time) {
	fi, err := os.Stat(filepath.Join(x.project, name))
	if err != nil || !fi.ModTime().After(at) {
		t.Errorf("%s missing or written before %v: %v", name, at.Format(time.TimeOnly), err)
	}
}

// runnerPID finds this session's queue runner: a `gantline q --run` from
// this test's binary dir whose environment names this tmux socket.
func (x *sess) runnerPID(t *testing.T) int {
	self := filepath.Join(x.bin, "gantline")
	pids, _ := proc.Pids()
	for _, pid := range pids {
		args, err := proc.Argv(pid)
		if err != nil {
			continue
		}
		if len(args) < 3 || args[0] != self || args[1] != "q" || args[2] != "--run" {
			continue
		}
		env, _ := proc.Env(pid)
		if !slices.Contains(env, "GANTLINE_TMUX_SOCKET="+x.s.Socket) {
			continue
		}
		return pid
	}
	t.Fatal("no runner process found")
	return 0
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}
