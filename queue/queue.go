// Package queue keeps a pane's queue of text to type once its turn ends,
// runs it, and checks what each command actually did.
//
// The queue is <PaneDir>/queue.jsonl, append-only: an "add" record per
// item, then "set" records as it runs. Items survive the process that
// queued them (G9). One runner per pane, elected by the flock runner.lock,
// takes pending items in order.
package queue

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justinstimatze/gantline/journal"
)

// States an item moves through. Everything but Pending and Running is final.
const (
	Pending     = "pending"
	Running     = "running"
	Done        = "done"        // the outcome was confirmed from hooks or the transcript
	Rejected    = "rejected"    // Claude Code ran it and said no (unknown model, no such server)
	Failed      = "failed"      // gantline couldn't type it
	Unconfirmed = "unconfirmed" // typed and submitted; nothing confirmed what it did
	Expired     = "expired"     // the pane never became idle before the timeout
)

// Final reports whether state is terminal.
func Final(state string) bool { return state != Pending && state != Running && state != "" }

// Item is one queued entry, folded from its records.
type Item struct {
	ID        string `json:"id"`
	Cmd       string `json:"cmd"`
	Then      string `json:"then,omitempty"`
	Model     string `json:"model,omitempty"`       // set: relaunch the session with this model instead of typing Cmd
	Enq       int64  `json:"enq"`                   // unix ns
	Timeout   int64  `json:"timeout_ns"`            // how long to wait for idle, from Enq
	IdleAtEnq bool   `json:"idle_at_enq,omitempty"` // the pane was idle when queued: no turn end to wait for
	State     string `json:"state"`
	Detail    string `json:"detail,omitempty"`
	ThenState string `json:"then_state,omitempty"`
	Updated   int64  `json:"updated,omitempty"`
	Typed     int64  `json:"typed,omitempty"`  // unix ns when the command was submitted
	Screen    string `json:"screen,omitempty"` // the block above the box when it finished
	Reported  bool   `json:"reported,omitempty"`
}

// EnqTime is when the item was queued.
func (it Item) EnqTime() time.Time { return time.Unix(0, it.Enq) }

// Deadline is when the item stops waiting for the pane to become idle.
func (it Item) Deadline() time.Time { return it.EnqTime().Add(time.Duration(it.Timeout)) }

type record struct {
	Op string `json:"op"` // add, set, reported
	Item
}

func path(key string) string { return filepath.Join(journal.PaneDir(key), "queue.jsonl") }

// ErrPicker refuses a command that opens a picker gantline can't drive.
var ErrPicker = errors.New("queue: this command opens a picker gantline can't navigate")

// ErrModelDefault refuses `/model <name>`: besides switching this session it
// saves the model as the default for every new session.
var ErrModelDefault = errors.New("queue: `/model <name>` also changes the default model for every new session; use `gantline model <name>`, which relaunches only this session")

// ErrUnchecked refuses a slash command gantline hasn't run against a real
// session. Many commands open a dialog when they lack an argument, and the
// runner never presses a key to close one, so the session would wait for a
// person.
var ErrUnchecked = errors.New("queue: gantline hasn't checked this command against a real session")

// ErrNever refuses a command a session must not run on itself, with or
// without --unchecked.
var ErrNever = errors.New("queue: a session must not run this on itself")

// checked lists the slash commands, and the argument forms of each, that a
// real session has run through `gantline q` (the e2e tests and demo/), keyed
// by command name. Each func reports whether the arguments are a checked
// form.
var checked = map[string]func(args []string) bool{
	"/compact": func([]string) bool { return true },
	"/clear":   func(a []string) bool { return len(a) == 0 },
	"/context": func(a []string) bool { return len(a) == 0 },
	"/mcp":     func(a []string) bool { return len(a) == 2 && a[0] == "reconnect" },
	"/color": func(a []string) bool {
		return len(a) == 1 && strings.Contains(" red blue green yellow purple orange pink cyan default ", " "+a[0]+" ")
	},
}

// never lists commands no flag lets through, with the reason.
var never = map[string]string{
	"/exit":       "it ends the session; use `gantline restart` to restart it",
	"/quit":       "it ends the session; use `gantline restart` to restart it",
	"/logout":     "it signs out every session on the machine",
	"/login":      "it opens a sign-in flow that needs a person",
	"/background": "it detaches the session from its pane, so nothing could confirm the outcome",
}

// Guard refuses a command unless it is plain text or a checked slash command
// form. unchecked lets other slash commands through, except bare /mcp and
// /model (pickers), any /model (it rewrites the global default model),
// /config with arguments (it writes user settings), and the never list.
func Guard(cmd string, unchecked bool) error {
	f := strings.Fields(cmd)
	if len(f) == 0 || !strings.HasPrefix(f[0], "/") {
		return nil
	}
	name, args := f[0], f[1:]
	switch {
	case name == "/mcp" && len(args) == 0:
		return fmt.Errorf("%w; use `/mcp reconnect <server>`", ErrPicker)
	case name == "/model" && len(args) == 0:
		return fmt.Errorf("%w; use `gantline model <name>`", ErrPicker)
	case name == "/model":
		return ErrModelDefault
	case name == "/config" && len(args) > 0:
		return fmt.Errorf("%w: it writes your user settings, which every session reads; edit the settings file instead", ErrNever)
	}
	if why, ok := never[name]; ok {
		return fmt.Errorf("%w: %s", ErrNever, why)
	}
	if ok := checked[name]; ok != nil && ok(args) {
		return nil
	}
	if unchecked {
		return nil
	}
	return fmt.Errorf("%w (checked: /compact [instructions], /clear, /context, /mcp reconnect <server>, /color <color>). "+
		"If it opens a dialog, the session waits for a person to close it. To queue it anyway, pass --unchecked", ErrUnchecked)
}

// Add appends a pending item and returns it with its id (q1, q2, …). It
// refuses what Guard refuses without unchecked.
func Add(key, cmd, then string, timeout time.Duration, idleAtEnq bool, now time.Time) (Item, error) {
	return AddUnchecked(key, cmd, then, timeout, idleAtEnq, now, false)
}

// AddUnchecked is Add with Guard's unchecked flag.
func AddUnchecked(key, cmd, then string, timeout time.Duration, idleAtEnq bool, now time.Time, unchecked bool) (Item, error) {
	if err := Guard(cmd, unchecked); err != nil {
		return Item{}, err
	}
	return add(key, Item{Cmd: cmd, Then: then}, timeout, idleAtEnq, now)
}

// AddModel queues a relaunch of the pane's session with model: at a turn
// end, the runner restarts claude in place with --resume and --model (see
// package relaunch). Unlike `/model`, it leaves the default model in
// ~/.claude/settings.json alone.
func AddModel(key, model, then string, timeout time.Duration, idleAtEnq bool, now time.Time) (Item, error) {
	if model == "" || strings.HasPrefix(model, "-") || strings.ContainsAny(model, " \t\n") {
		return Item{}, fmt.Errorf("queue: bad model name %q", model)
	}
	return add(key, Item{Cmd: "relaunch --model " + model, Then: then, Model: model}, timeout, idleAtEnq, now)
}

func add(key string, it Item, timeout time.Duration, idleAtEnq bool, now time.Time) (Item, error) {
	release, err := journal.Lock(key, "queue.lock", 10*time.Second)
	if err != nil {
		return Item{}, err
	}
	defer release()
	items, err := Items(key)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Item{}, err
	}
	it.ID, it.Enq, it.Timeout, it.IdleAtEnq, it.State = fmt.Sprintf("q%d", len(items)+1), now.UnixNano(), int64(timeout), idleAtEnq, Pending
	return it, write(key, record{Op: "add", Item: it})
}

// maxDetail caps a stored detail; a /context panel is several KB.
const maxDetail = 300

// Set records a new state (and detail, cut to maxDetail runes) for id.
func Set(key, id, state, detail string) error {
	if r := []rune(detail); len(r) > maxDetail {
		detail = string(r[:maxDetail-1]) + "…"
	}
	return write(key, record{Op: "set", Item: Item{ID: id, State: state, Detail: detail, Updated: time.Now().UnixNano()}})
}

// SetThen records the outcome of id's follow-up text.
func SetThen(key, id, state string) error {
	return write(key, record{Op: "set", Item: Item{ID: id, ThenState: state, Updated: time.Now().UnixNano()}})
}

// SetTyped records when id's command was submitted, so its output can be
// found in the transcript later (Output).
func SetTyped(key, id string, t time.Time) error {
	return write(key, record{Op: "set", Item: Item{ID: id, Typed: t.UnixNano(), Updated: time.Now().UnixNano()}})
}

// SetScreen records what the pane showed above the input box when id's
// command finished.
func SetScreen(key, id, text string) error {
	return write(key, record{Op: "set", Item: Item{ID: id, Screen: text, Updated: time.Now().UnixNano()}})
}

// ScreenOutput returns what the pane showed above the input box when id's
// command finished. In the alternate screen Claude Code draws in, tmux
// keeps no scrollback: a short pane's panel is gone once the next turn
// draws over it, so this copy is taken before any follow-up is typed.
func ScreenOutput(key, id string) (string, error) {
	items, err := Items(key)
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if it.ID == id {
			if it.Screen == "" {
				return "", fmt.Errorf("queue: no screen copy for %s (state %s)", id, it.State)
			}
			return it.Screen, nil
		}
	}
	return "", fmt.Errorf("queue: no item %s", id)
}

// Output returns the full output Claude Code recorded in the transcript for
// id's command (a local command such as /context): every
// <local-command-stdout> after it was submitted, oldest first. The screen
// can't be relied on for this: a short pane scrolls or redraws it away.
func Output(key, id string) (string, error) {
	items, err := Items(key)
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if it.ID != id {
			continue
		}
		if it.Typed == 0 {
			return "", fmt.Errorf("queue: %s has not been typed (state %s)", id, it.State)
		}
		outs := LocalOutputs(transcript(key), time.Unix(0, it.Typed))
		if len(outs) == 0 {
			return "", fmt.Errorf("queue: no output in the transcript for %s", id)
		}
		return strings.Join(outs, "\n\n"), nil
	}
	return "", fmt.Errorf("queue: no item %s", id)
}

// MarkReported records that id's outcome reached the agent.
func MarkReported(key, id string) error {
	return write(key, record{Op: "reported", Item: Item{ID: id}})
}

func write(key string, r record) error {
	if err := os.MkdirAll(journal.PaneDir(key), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path(key), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// Items folds the queue file into its items, oldest first.
func Items(key string) ([]Item, error) {
	f, err := os.Open(path(key))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var items []Item
	index := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r record
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.ID == "" {
			continue
		}
		i, seen := index[r.ID]
		switch {
		case r.Op == "add" && !seen:
			index[r.ID] = len(items)
			items = append(items, r.Item)
		case r.Op == "set" && seen:
			if r.State != "" {
				items[i].State, items[i].Detail = r.State, r.Detail
			}
			if r.ThenState != "" {
				items[i].ThenState = r.ThenState
			}
			if r.Typed != 0 {
				items[i].Typed = r.Typed
			}
			if r.Screen != "" {
				items[i].Screen = r.Screen
			}
			items[i].Updated = r.Updated
		case r.Op == "reported" && seen:
			items[i].Reported = true
		}
	}
	return items, sc.Err()
}

// Next returns the oldest pending item.
func Next(items []Item) (Item, bool) {
	for _, it := range items {
		if it.State == Pending {
			return it, true
		}
	}
	return Item{}, false
}

// Kind classifies a command for outcome checking.
func Kind(cmd string) string {
	f := strings.Fields(cmd)
	switch {
	case len(f) == 0:
		return "empty"
	case f[0] == "/compact":
		return "compact"
	case f[0] == "/model" && len(f) > 1:
		return "model"
	case f[0] == "/clear":
		return "clear"
	case f[0] == "/mcp" && len(f) > 2 && f[1] == "reconnect":
		return "mcp-reconnect"
	case strings.HasPrefix(f[0], "/"):
		return "slash"
	default:
		return "prompt"
	}
}

// Footer returns one line per final, unreported item, at most max, for the
// agent's next prompt (law 10), and the ids it covers.
func Footer(items []Item, max int) (lines, ids []string) {
	for _, it := range items {
		if !Final(it.State) || it.Reported {
			continue
		}
		if len(lines) == max {
			break
		}
		l := fmt.Sprintf("gantline: %s %s %s", it.ID, clip(it.Cmd, 40), it.State)
		if it.Detail != "" {
			l += " (" + clip(it.Detail, 80) + ")"
		}
		if it.ThenState != "" {
			l += "; follow-up " + it.ThenState
		}
		lines, ids = append(lines, l), append(ids, it.ID)
	}
	return lines, ids
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
