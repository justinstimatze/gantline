package queue

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/justinstimatze/gantline/internal/audit"
	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/relaunch"
	"github.com/justinstimatze/gantline/say"
	"github.com/justinstimatze/gantline/screen"
	"github.com/justinstimatze/gantline/tmux"
)

// RunOptions tunes Run. Zero fields take the defaults.
type RunOptions struct {
	Say    say.Options // Lock and IdleOnly are always set by Run
	Verify VerifyOptions
	Turn   journal.WaitOptions // the turn-end wait; Timeout comes from each item
	Poll   time.Duration       // idle poll; default 500ms
	// DropWait is the pause before typing again when no key reached the
	// box (say.ErrDropped); default 3s, up to maxDrops times.
	DropWait time.Duration
	// FindSession reads the pane's claude process for a relaunch; nil
	// means relaunch.Find. Tests point it at a fake.
	FindSession func(panePID int) (relaunch.Session, error)
	Log         func(format string, a ...any)
}

// Run is the pane's queue runner. It returns at once if another runner holds
// runner.lock. Otherwise it takes pending items in order until none are
// left. After releasing the lock it looks once more, so an item queued just
// as it finished is never stranded: either it sees the item, or the item's
// own runner gets the lock.
func Run(s tmux.Server, pane, key string, o RunOptions) error {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Poll <= 0 {
		o.Poll = 500 * time.Millisecond
	}
	if o.DropWait <= 0 {
		o.DropWait = 3 * time.Second
	}
	for {
		release, err := journal.Lock(key, "runner.lock", 0)
		if errors.Is(err, journal.ErrLocked) {
			return nil
		}
		if err != nil {
			return err
		}
		r := runner{s: s, pane: pane, key: key, o: o}
		// The lock means no other runner is alive, so an item still marked
		// running lost its runner partway through (killed, or the host
		// rebooted). Its keys may already be in, so it is never retyped.
		if items, err := Items(key); err == nil {
			for _, it := range items {
				if it.State == Running {
					Set(key, it.ID, Unconfirmed, errOrphaned)
					o.Log("%s %q: %s %s", it.ID, it.Cmd, Unconfirmed, errOrphaned)
				}
			}
		}
		for {
			items, err := Items(key)
			if err != nil {
				release()
				return err
			}
			it, ok := Next(items)
			if !ok {
				break
			}
			r.run(it)
		}
		release()
		items, _ := Items(key)
		if _, ok := Next(items); !ok {
			return nil
		}
	}
}

type runner struct {
	s          tmux.Server
	pane, key  string
	o          RunOptions
	lastPrompt time.Time // when this runner last typed a prompt that starts a turn
	cur        string    // the item being worked, for the keys audit
	tail       bool      // the last submit was confirmed from the box's end only
}

func (r *runner) run(it Item) {
	start := time.Now()
	r.cur = it.ID
	Set(r.key, it.ID, Running, "")
	r.o.Log("%s %q: waiting for the turn to end", it.ID, it.Cmd)
	state, detail, enters := r.attempt(it)
	Set(r.key, it.ID, state, detail)
	r.o.Log("%s %q: %s %s", it.ID, it.Cmd, state, detail)
	audit.Write(audit.Rec{Kind: "q", Pane: r.pane, Bytes: len(it.Cmd), Enters: enters, Took: time.Since(start),
		Extra: map[string]any{"id": it.ID, "outcome": state, "cmd_kind": itemKind(it)}})
	if state != Done || it.Then == "" {
		return
	}
	r.o.Log("%s: typing the follow-up", it.ID)
	thenState := Done
	// Stamp before typing: the turn this starts can end before Submit
	// returns, and the next item waits for a Stop after this stamp.
	before := time.Now()
	if _, err := r.typeWhenIdle(it.Then, time.Now().Add(time.Duration(it.Timeout))); err != nil {
		thenState = Failed
		r.o.Log("%s follow-up: %v", it.ID, err)
	} else {
		r.lastPrompt = before
	}
	SetThen(r.key, it.ID, thenState)
}

// attempt waits for the pane to be idle, types it.Cmd and verifies it.
func (r *runner) attempt(it Item) (state, detail string, enters int) {
	deadline := it.Deadline()
	// Text queued during a turn waits for that turn's Stop (and for the
	// turn of any prompt this runner typed since). Text queued into an idle
	// pane has no turn to wait for.
	since := turnStart(r.key, it.EnqTime())
	if r.lastPrompt.After(since) {
		since = r.lastPrompt
	}
	if !it.IdleAtEnq || !r.lastPrompt.IsZero() {
		w := r.o.Turn
		w.Timeout = time.Until(deadline)
		if err := journal.WaitTurnEnd(r.key, since, w); err != nil {
			return Expired, "no turn end before the timeout", 0
		}
	}
	if it.Model != "" {
		state, detail := r.relaunchSession(it, deadline)
		return state, detail, 0
	}
	typed := time.Now()
	n, err := r.typeWhenIdle(it.Cmd, deadline)
	if err == nil {
		SetTyped(r.key, it.ID, typed)
	}
	if err != nil {
		if errors.Is(err, errExpired) {
			r.keepBottom(it.ID)
			return Expired, err.Error(), n
		}
		if errors.Is(err, errQueuedByClaude) {
			// A queued /compact runs when the turn ends: watch for it until
			// the item's deadline rather than reporting it unconfirmed.
			if Kind(it.Cmd) == "compact" {
				r.o.Log("%s %q: may be in Claude Code's input queue; watching for it to run", it.ID, it.Cmd)
				v := r.o.Verify
				v.Start = time.Until(deadline)
				if out := Verify(r.key, it.Cmd, typed, v); out.State == Done {
					return Done, out.Detail + " (from Claude Code's input queue)", n
				}
			}
			return Unconfirmed, err.Error(), n
		}
		return Failed, err.Error(), n
	}
	// A command can open a confirmation (/model mid-conversation asks
	// "Switch model?"). Answering is a person's call (law 4), so record
	// what is waiting and give them until the item's timeout.
	if title := r.dialogTitle(); title != "" {
		Set(r.key, it.ID, Running, "waiting for a person to answer: "+title)
		r.o.Log("%s %q: dialog open, waiting for a person: %s", it.ID, it.Cmd, title)
		for r.dialogTitle() != "" {
			if time.Now().After(deadline) {
				return Unconfirmed, "dialog left unanswered: " + title, n
			}
			time.Sleep(r.o.Poll)
		}
	}
	out := Verify(r.key, it.Cmd, typed, r.o.Verify)
	if out.State == Done && r.tail {
		// Only the box's end was on screen: compare what Claude Code
		// recorded with what was typed.
		want := strings.TrimSpace(strings.TrimPrefix(it.Cmd, "/compact"))
		got, found := CompactArgs(transcript(r.key), typed)
		switch {
		case !found:
			out.Detail += " (only the end of the text could be read on screen, and the transcript has no record of the instructions)"
			out.State = Unconfirmed
		case strings.Join(strings.Fields(got), " ") != strings.Join(strings.Fields(want), " "):
			out.State = Failed
			out.Detail = fmt.Sprintf("compacted, but with instructions that differ from the typed ones (%d chars recorded, %d typed): only the end of the box was on screen", len(got), len(want))
		default:
			out.Detail += " (instructions checked against the transcript; the box was taller than the pane)"
		}
	}
	if out.State == Done && strings.HasPrefix(it.Cmd, "/") {
		if c, err := r.s.Capture(r.pane); err == nil {
			if lines, _, _ := screen.Cut(c).Region("last"); len(lines) > 0 {
				SetScreen(r.key, it.ID, capScreen(strings.Join(lines, "\n")))
			}
		}
	}
	if Kind(it.Cmd) == "prompt" {
		r.lastPrompt = typed
	}
	return out.State, out.Detail, n
}

const errOrphaned = "its runner stopped partway through; not typed again, as the keys may already have gone in"

var errExpired = errors.New("pane not idle before the timeout")

// errQueuedByClaude: keys that never showed in the box may be waiting in
// Claude Code's input queue, so the runner doesn't type them again.
var errQueuedByClaude = errors.New("the keys never showed in the box, and the session was busy or queued input; not typing again, as it may run later")

// maybeQueued reports whether keys typed from begin could be sitting in
// Claude Code's own input queue: the journal says the session was busy
// then, or the transcript shows input queued since.
func (r *runner) maybeQueued(begin time.Time) bool {
	evs, _ := journal.Events(r.key)
	return len(journal.BusyAt(evs, begin)) > 0 || QueuedSince(transcript(r.key), begin)
}

// lateWait bounds how long dropped keys are watched for in the box.
const lateWait = time.Minute

// submitLate watches, for up to lateWait, for keys that never showed in the
// box to arrive there once the session is idle, and presses Enter on them.
// It stops without pressing anything when the box shows other text.
func (r *runner) submitLate(text string, deadline time.Time) (int, bool, error) {
	if d := time.Now().Add(lateWait); d.Before(deadline) {
		deadline = d
	}
	opt := r.o.Say
	for ; time.Now().Before(deadline); time.Sleep(r.o.Poll) {
		if r.waitIdle(deadline) != nil {
			return 0, false, nil
		}
		n, ok, err := say.SubmitOwn(r.s, r.pane, text, opt)
		if ok || err != nil {
			if ok {
				r.o.Log("its keys reached the box after the turn; pressed Enter")
			}
			return n, ok, err
		}
		if c, err := r.s.Capture(r.pane); err == nil {
			if in, _ := screen.Input(c); in != "" && !screen.SameText(in, text) {
				return 0, false, nil // someone else's text: leave it
			}
		}
	}
	return 0, false, nil
}

// maxDrops bounds retyping after say.ErrDropped.
const maxDrops = 5

// typeWhenIdle waits for the screen to be idle, then submits text with the
// pane's write lock held and IdleOnly set. A turn that starts in between, or
// a person's draft in the box, sends it back to waiting; a draft is never
// cleared. Whatever goes wrong, it never leaves its own unsent text in the
// box, where the next thing anyone types would join it.
func (r *runner) typeWhenIdle(text string, deadline time.Time) (n int, err error) {
	defer func() {
		if err != nil && say.ClearOwn(r.s, r.pane, text) {
			r.o.Log("cleared its own unsent text from the box")
		}
	}()
	opt := r.o.Say
	opt.Lock, opt.IdleOnly = r.key, true
	// A /compact taller than the pane may show only its end (macOS); the
	// result is then checked against the transcript (attempt).
	r.tail = false
	if Kind(text) == "compact" {
		opt.Tail = &r.tail
	}
	drops := 0
	waiting := "" // what the runner last said it waits on
	for {
		if err := r.waitIdle(deadline); err != nil {
			return 0, err
		}
		begin := time.Now()
		n, err := say.Submit(r.s, r.pane, text, opt)
		switch {
		case err == nil:
			// One line per delivery, so `gantline audit` can check each
			// against the journal at the moment typing began.
			audit.Write(audit.Rec{Kind: "keys", Pane: r.pane, Bytes: len(text), Enters: n, Took: time.Since(begin),
				Extra: map[string]any{"key": r.key, "id": r.cur, "cmd_kind": Kind(text), "start_ns": begin.UnixNano()}})
			return n, nil
		case errors.Is(err, say.ErrDropped) && r.maybeQueued(begin):
			// The keys may run later from Claude Code's queue, or land in
			// the box once the turn ends and sit there unsent. Only the
			// second needs Enter, and the box shows which.
			if m, ok, err := r.submitLate(text, deadline); ok || err != nil {
				return m, err
			}
			return n, fmt.Errorf("%w (%v)", errQueuedByClaude, err)
		case errors.Is(err, say.ErrDropped) && drops < maxDrops && time.Now().Add(r.o.DropWait).Before(deadline):
			drops++
			r.o.Log("no key reached the box (%d of %d); typing again in %s", drops, maxDrops, r.o.DropWait)
			time.Sleep(r.o.DropWait)
		case errors.Is(err, say.ErrDraft) && say.ClearOwn(r.s, r.pane, text):
			// Its own keys from an attempt that couldn't submit, not a
			// person's draft: cleared, so the next pass types them again.
			r.o.Log("cleared its own unsubmitted text from the box")
		case errors.Is(err, say.ErrBusy), errors.Is(err, say.ErrDraft), errors.Is(err, say.ErrDialog),
			errors.Is(err, say.ErrNoInputBox), errors.Is(err, journal.ErrLocked):
			if time.Now().After(deadline) {
				return n, fmt.Errorf("%w (last: %v)", errExpired, err)
			}
			// Say once what it waits on, in q.log and in the item's detail
			// for `gantline status`, so a wait that lasts to the timeout
			// isn't silent. The text of a draft is never logged.
			if why := err.Error(); why != waiting {
				waiting = why
				r.o.Log("%s: waiting until %s: %s", r.cur, deadline.Format(time.TimeOnly), why)
				Set(r.key, r.cur, Running, "waiting: "+why)
			}
			time.Sleep(r.o.Poll)
		default:
			return n, err
		}
	}
}

// waitIdle polls until the screen is idle on two polls in a row: not busy,
// and showing its input box. A dialog hides both the box and the busy mode
// line; it waits for a person, so this waits too.
func (r *runner) waitIdle(deadline time.Time) error {
	idle, why := 0, ""
	for ; time.Now().Before(deadline); time.Sleep(r.o.Poll) {
		c, err := r.s.Capture(r.pane)
		if err != nil {
			return err
		}
		_, box := screen.Input(c)
		switch {
		case screen.Busy(c):
			why = "the screen shows a turn running"
		case !box:
			why = "no input box on screen"
		case r.compacting():
			why = "a compaction is open in the journal"
		default:
			why = ""
		}
		if why != "" {
			idle = 0
			continue
		}
		if idle++; idle >= 2 {
			return nil
		}
	}
	if why == "" {
		return errExpired
	}
	return fmt.Errorf("%w (last: %s)", errExpired, why)
}

// keepBottom stores the bottom of the screen with an expired item, so
// `q --show <id> --screen` shows what kept the pane from looking idle.
func (r *runner) keepBottom(id string) {
	c, err := r.s.Capture(r.pane)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(c, "\n "), "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	SetScreen(r.key, id, capScreen(strings.Join(lines, "\n")))
}

// compacting reports whether the journal shows a compaction still open.
// Keys typed during one never reach the box, and a background agent's
// tool calls can keep the screen from showing it.
func (r *runner) compacting() bool {
	evs, _ := journal.Events(r.key)
	return journal.CompactionOpen(evs, time.Now())
}

// dialogTitle is the title of a dialog on screen, or "" when the input box
// is showing.
func (r *runner) dialogTitle() string {
	c, err := r.s.Capture(r.pane)
	if err != nil {
		return ""
	}
	l := screen.Cut(c)
	if !l.Box.Empty() || l.Dialog.Empty() {
		return ""
	}
	if t := screen.DialogTitle(l); t != "" {
		return t
	}
	return "(untitled dialog)"
}

// turnStart is when the turn running at t began: the last UserPromptSubmit
// at or before t, or t when there is none. Waiting for a turn end after
// this, not after t, keeps an item queued just after a Stop (the same
// millisecond, in one e2e run) from waiting for a turn that never comes.
func turnStart(key string, t time.Time) time.Time {
	evs, _ := journal.Events(key)
	start := t
	for _, e := range evs {
		if e.Time().After(t) {
			break
		}
		if e.Ev == "UserPromptSubmit" {
			start = e.Time().Add(-time.Nanosecond)
		}
	}
	return start
}

func itemKind(it Item) string {
	if it.Model != "" {
		return "relaunch"
	}
	return Kind(it.Cmd)
}

// maxScreen caps the screen block kept with an item in queue.jsonl.
const maxScreen = 8 << 10

// capScreen keeps the first maxScreen bytes of a screen block, cut at a
// line end.
func capScreen(s string) string {
	if len(s) <= maxScreen {
		return s
	}
	s = s[:maxScreen]
	if i := strings.LastIndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	return s + "\n… (cut at 8 KiB)"
}
