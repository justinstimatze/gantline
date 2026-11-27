// Package say submits a message into a Claude Code pane and confirms it went
// in. It is the checked replacement for typing with raw send-keys.
package say

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/screen"
	"github.com/justinstimatze/gantline/tmux"
)

var (
	// ErrNoInputBox means the pane shows no Claude Code input box.
	ErrNoInputBox = errors.New("say: no Claude Code input box on screen")
	// ErrDraft means the input box already holds text, probably a person's
	// unfinished message. Nothing is typed over it.
	ErrDraft = errors.New("say: input box already holds a draft; not typing over it")
	// ErrMultiline means the text has a newline, which would submit early.
	ErrMultiline = errors.New("say: text contains a newline")
	// ErrEcho means the input box did not show the typed text. The box is
	// cleared again before returning.
	ErrEcho = errors.New("say: typed text did not appear in the input box")
	// ErrDropped is ErrEcho with the box still empty: no typed key reached
	// Claude Code. Seen just after a resumed session starts and during an
	// auto-compaction; the keys were dropped, never replayed later, so
	// typing again is safe.
	ErrDropped = fmt.Errorf("%w; none of it reached the box", ErrEcho)
	// ErrNotSubmitted means the text stayed in the box after every Enter.
	ErrNotSubmitted = errors.New("say: text still in the input box after every Enter")
	// ErrDialog means a recognised dialog (a permission prompt, the folder
	// trust check) is on screen. Typed keys would answer it, so nothing is
	// typed.
	ErrDialog = errors.New("say: a dialog is on screen; not typing into it")
	// ErrBusy means Options.IdleOnly was set and the pane is mid-turn.
	ErrBusy = errors.New("say: pane is busy")
)

// Options tunes Submit. Zero fields take the defaults.
type Options struct {
	Type     tmux.TypeOptions
	Attempts int           // Enter presses before giving up. Default 6.
	Wait     time.Duration // how long each Enter gets to empty the box. Default 2s.
	Poll     time.Duration // screen poll interval. Default 100ms.

	// Lock is the pane's journal key. When set, Submit holds the pane's
	// write lock (journal.LockWrites) from its first screen check to its
	// last Enter, waiting up to LockWait (default 60s) for it.
	Lock     string
	LockWait time.Duration
	// IdleOnly refuses with ErrBusy when the pane shows a turn running.
	// Without it, text typed into a busy pane is queued by Claude Code.
	IdleOnly bool
	// PasteOver sends text of at least this many bytes as one bracketed
	// paste instead of typing it. Default 800, below the 850-byte burst
	// where Claude Code starts collapsing typed input anyway. Negative
	// never pastes. A slash command is always typed: Claude Code takes a
	// pasted one as prompt text and never runs it.
	PasteOver int
}

func (o Options) withDefaults() Options {
	if o.Attempts <= 0 {
		o.Attempts = 6
	}
	if o.Wait <= 0 {
		o.Wait = 2 * time.Second
	}
	if o.Poll <= 0 {
		o.Poll = 100 * time.Millisecond
	}
	if o.LockWait <= 0 {
		o.LockWait = 60 * time.Second
	}
	if o.PasteOver == 0 {
		o.PasteOver = 800
	}
	return o
}

// Submit types text into pane's input box and presses Enter until the box
// empties, returning how many Enter presses that took.
//
// It refuses if a recognised dialog is on screen or the box already holds a
// draft, and after typing it checks the box shows the text. A busy pane
// accepts the message as queued, which counts as submitted unless
// opt.IdleOnly is set. Text of opt.PasteOver bytes or more, other than a
// slash command, goes in as one bracketed paste; the box then shows "[Pasted text #N]", which counts as
// the echo.
//
// It never presses a key to dismiss a "Heads up" notice: no real capture of
// one exists yet, and the only pattern known ("0: Dismiss") also matches
// ordinary conversation text. A real notice that eats keys makes the echo
// check fail, the box is cleared, and the error says a notice may be up.
func Submit(s tmux.Server, pane, text string, opt Options) (int, error) {
	opt = opt.withDefaults()
	if strings.ContainsAny(text, "\r\n") {
		return 0, ErrMultiline
	}
	if opt.Lock != "" {
		release, err := journal.LockWrites(opt.Lock, opt.LockWait)
		if err != nil {
			return 0, err
		}
		defer release()
	}
	capture, err := s.Capture(pane)
	if err != nil {
		return 0, err
	}
	if id := screen.Dialog(screen.Cut(capture)); id != "" {
		return 0, fmt.Errorf("%w (%s)", ErrDialog, id)
	}
	if opt.IdleOnly && screen.Busy(capture) {
		return 0, ErrBusy
	}
	draft, found := screen.Input(capture)
	if !found {
		return 0, ErrNoInputBox
	}
	if draft != "" {
		return 0, ErrDraft
	}

	if opt.PasteOver > 0 && len(text) >= opt.PasteOver && !strings.HasPrefix(text, "/") {
		if err := s.Paste(pane, text); err != nil {
			return 0, err
		}
		if got, ok := waitInput(s, pane, opt, func(in string) bool {
			return screen.SameText(in, text) || screen.Pasted(in)
		}); !ok {
			clearBox(s, pane)
			return 0, fmt.Errorf("%w: box shows %q after a paste", echoErr(got), clip(got))
		}
		if err := stillIdle(s, pane, opt); err != nil {
			return 0, err
		}
		return pressEnter(s, pane, text, opt)
	}

	// Under host load several chunks can reach Claude Code in one read and
	// collapse into a "[Pasted text #N]" placeholder. Then clear and retype
	// slower: half the chunk, four times the gap, up to three more times.
	typeOpt := opt.Type
	for try := 0; ; try++ {
		if err := s.Type(pane, text, typeOpt); err != nil {
			return 0, err
		}
		got, ok := waitInput(s, pane, echoOpt(opt, text), func(in string) bool {
			return screen.SameText(in, text) || screen.Pasted(in)
		})
		if ok && !screen.Pasted(got) {
			break
		}
		clearBox(s, pane)
		if !ok || try == 3 {
			if c, err := s.Capture(pane); err == nil && screen.HeadsUp(c) {
				return 0, fmt.Errorf("%w: box shows %q (a \"Heads up\" notice may be eating keys; dismiss it by hand)", ErrEcho, clip(got))
			}
			return 0, fmt.Errorf("%w: box shows %q", echoErr(got), clip(got))
		}
		typeOpt = slower(typeOpt)
	}
	if err := stillIdle(s, pane, opt); err != nil {
		return 0, err
	}
	return pressEnter(s, pane, text, opt)
}

// stillIdle re-checks, with the text in the box and before Enter, that no
// turn started while it was typed: a background task's notification can
// start one at any moment, and Enter during a turn queues the text, where a
// pasted slash command arrives later as plain prompt text. On a turn it
// clears the box (the text in it is ours, just checked) and returns ErrBusy.
func stillIdle(s tmux.Server, pane string, opt Options) error {
	if !opt.IdleOnly {
		return nil
	}
	c, err := s.Capture(pane)
	if err != nil {
		return err
	}
	if screen.Busy(c) {
		clearBox(s, pane)
		return fmt.Errorf("%w: a turn started while typing", ErrBusy)
	}
	return nil
}

// echoOpt gives typed text 2ms per byte more to show in the box than
// opt.Wait: a 1,000-byte /compact renders key by key, which a loaded host
// or the race detector stretches past a fixed wait. waitInput returns as
// soon as the text shows, so the extra time costs nothing when it does.
func echoOpt(opt Options, text string) Options {
	opt.Wait += time.Duration(len(text)) * 2 * time.Millisecond
	return opt
}

// echoErr is ErrDropped when the box stayed empty, else ErrEcho.
func echoErr(got string) error {
	if got == "" {
		return ErrDropped
	}
	return ErrEcho
}

// pressEnter presses Enter until the box empties, up to opt.Attempts times.
//
// It presses again only while the box still shows what was put in (the
// typed text, or the paste placeholder): that is a swallowed Enter. If the
// box shows anything else, an Enter did something other than submit (on a
// partial slash command it picks from the autocomplete menu), and another
// press could pick again, so it stops.
func pressEnter(s tmux.Server, pane, text string, opt Options) (int, error) {
	unchanged := func(in string) bool { return screen.SameText(in, text) || screen.Pasted(in) }
	for attempt := 1; attempt <= opt.Attempts; attempt++ {
		if err := s.Enter(pane); err != nil {
			return attempt, err
		}
		got, ok := waitInput(s, pane, opt, func(in string) bool { return in == "" })
		if ok {
			return attempt, nil
		}
		// The box was replaced (a confirmation dialog, such as /model's
		// "Switch model?"): the text left the box, so it was submitted.
		if c, err := s.Capture(pane); err == nil {
			if _, found := screen.Input(c); !found {
				return attempt, nil
			}
		}
		if !unchanged(got) {
			return attempt, fmt.Errorf("%w: after Enter the box shows %q; not pressing again", ErrNotSubmitted, clip(got))
		}
	}
	return opt.Attempts, ErrNotSubmitted
}

// waitInput polls the input box until done(text) holds or opt.Wait passes,
// returning the last text seen.
func waitInput(s tmux.Server, pane string, opt Options, done func(string) bool) (string, bool) {
	var last string
	for deadline := time.Now().Add(opt.Wait); ; {
		if c, err := s.Capture(pane); err == nil {
			in, found := screen.Input(c)
			last = in
			if found && done(in) {
				return in, true
			}
		}
		if time.Now().After(deadline) {
			return last, false
		}
		time.Sleep(opt.Poll)
	}
}

// clearBox empties the input box. C-u clears one wrapped line per press.
// ClearOwn empties the input box if it holds exactly text: keys the caller
// typed itself and couldn't submit (a turn started, or Enter was dropped).
// It never touches anything else, which may be a person's draft. It reports
// whether the box is empty afterwards.
func ClearOwn(s tmux.Server, pane, text string) bool {
	c, err := s.Capture(pane)
	if err != nil {
		return false
	}
	in, ok := screen.Input(c)
	if !ok || in == "" || !screen.SameText(in, text) {
		return false
	}
	clearBox(s, pane)
	c, err = s.Capture(pane)
	if err != nil {
		return false
	}
	in, _ = screen.Input(c)
	return in == ""
}

// SubmitOwn presses Enter on text the caller typed that reached the box
// late and was never submitted: only if the pane is idle and the box holds
// exactly text, so it never submits a person's draft. ok is false, with
// nothing pressed, when the box holds anything else.
func SubmitOwn(s tmux.Server, pane, text string, opt Options) (n int, ok bool, err error) {
	opt = opt.withDefaults()
	c, err := s.Capture(pane)
	if err != nil {
		return 0, false, err
	}
	in, found := screen.Input(c)
	if !found || screen.Busy(c) || in == "" || !screen.SameText(in, text) {
		return 0, false, nil
	}
	n, err = pressEnter(s, pane, text, opt)
	return n, err == nil, err
}

func clearBox(s tmux.Server, pane string) {
	for range 60 {
		c, err := s.Capture(pane)
		if err != nil {
			return
		}
		if in, _ := screen.Input(c); in == "" {
			return
		}
		s.Run("send-keys", "-t", pane, "C-u")
		time.Sleep(50 * time.Millisecond)
	}
}

// slower halves the chunk size (floor 32 bytes) and quadruples the gap
// (floor 20ms) after a collapsed paste.
func slower(o tmux.TypeOptions) tmux.TypeOptions {
	chunk, gap := o.ChunkBytes, o.Gap
	if chunk <= 0 {
		chunk = 256
	}
	if gap <= 0 {
		gap = 5 * time.Millisecond
	}
	o.ChunkBytes = max(chunk/2, 32)
	o.Gap = max(gap*4, 20*time.Millisecond)
	return o
}

func clip(s string) string {
	s = strings.ReplaceAll(s, screen.Hidden, "…")
	if len(s) > 80 {
		return s[:40] + "…" + s[len(s)-40:]
	}
	return s
}
