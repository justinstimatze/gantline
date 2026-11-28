// Package screen reads Claude Code's terminal UI from captured pane text.
//
// Every pattern here was read off a real Claude Code 2.1.332 screen (the
// local corpus under ~/.local/state/gantline/corpus) or taken from an
// earlier pane-messaging script written against live panes.
package screen

import "strings"

// promptMarker starts the input-box line. Claude Code draws it as "❯"
// followed by U+00A0, not an ordinary space.
const promptMarker = "❯"

// Placeholders Claude Code shows in an empty input box.
var placeholders = []string{
	`Try "`, // e.g. Try "refactor <filepath>"
	"Press ↑ to edit queued messages",
	"Press up to edit queued messages",
}

// Input returns the text in the input box, joining lines the box wrapped,
// and whether an input box was found at all. An empty box, or one showing
// only a placeholder, returns "".
//
// The box is found by its landmarks (Cut): a ❯ line with a rule directly
// above and one below. A ❯ line in the conversation, such as the echo of
// the last prompt above a permission dialog, is not the box.
func Input(capture string) (text string, found bool) {
	l := Cut(capture)
	lines, _, _ := l.Region("box")
	if len(lines) == 0 {
		return "", false
	}
	rest, _ := strings.CutPrefix(lines[0], promptMarker)
	parts := []string{trim(rest)}
	for _, l := range lines[1:] {
		parts = append(parts, trim(l))
	}
	text = strings.Join(parts, "")
	for _, p := range placeholders {
		if strings.HasPrefix(text, p) {
			return "", true
		}
	}
	if l.HiddenAbove {
		text = Hidden + text
	}
	if l.HiddenBelow {
		text += Hidden
	}
	return text, true
}

// Hidden stands, in Input's text, for rows of a scrolled box that are off
// screen. It can't be typed or drawn, so text holding it is never mistaken
// for anything a person wrote; SameText matches the rows around it.
const Hidden = "\x00"

// Busy reports whether Claude Code shows it is working: while a turn, a
// compaction or the prompt hooks run, the mode line at the bottom carries an
// "esc to interrupt" segment. It is not always the last segment: with a
// background shell the line reads "… · esc to interrupt · ↓ to manage".
// Only the last few non-blank lines are checked, so the phrase in
// conversation text above the input box doesn't count. Verified against
// real 2.1.332 busy and idle captures (the "busy" fingerprint).
func Busy(capture string) bool {
	// With an input box, read its footer: a list of background agents can
	// sit below the mode line, pushing it out of the last few lines.
	// The in-progress spinner above the box ("✢ Catapulting… (1m 56s)")
	// counts on its own: a narrow pane can cut the mode line before the
	// segment ("⏵⏵ bypass permissions on · 4 backgr…").
	// Every footer row is read, not only the mode line: a notice can sit
	// where the mode line was, and a busy pane read as idle gets typed into.
	if l := Cut(capture); !l.Box.Empty() {
		if l.Spinner >= 0 {
			return true
		}
		for _, line := range l.Lines[l.Footer.Start:l.Footer.End] {
			if hasSegment(strings.TrimSpace(line), "esc to interrupt") {
				return true
			}
		}
		return false
	}
	lines := strings.Split(strings.TrimRight(capture, "\n "), "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < 3; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		seen++
		if hasSegment(l, "esc to interrupt") {
			return true
		}
	}
	return false
}

// Pasted reports whether input is Claude Code's collapsed-paste placeholder,
// e.g. "[Pasted text #3]" or "[Pasted text #3 +12 lines]". Claude Code shows
// it when one read from the terminal brings in a long burst, which under host
// load can happen even with chunked typing.
func Pasted(input string) bool {
	return strings.HasPrefix(input, "[Pasted text #")
}

// SameText reports whether input (read back from the box) matches typed,
// ignoring spaces: a space that falls on a line wrap is not drawn. When
// the box scrolled (input holds Hidden), the rows it shows must be the
// matching part of typed: its end when rows above are hidden, its start
// when rows below are, somewhere inside when both are.
func SameText(input, typed string) bool {
	in, t := strings.ReplaceAll(input, " ", ""), strings.ReplaceAll(typed, " ", "")
	above, below := strings.HasPrefix(in, Hidden), strings.HasSuffix(in, Hidden)
	in = strings.Trim(in, Hidden)
	switch {
	case !above && !below:
		return in == t
	case in == "":
		return false
	case above && below:
		return strings.Contains(t, in) && in != t
	case above:
		return strings.HasSuffix(t, in) && in != t
	default:
		return strings.HasPrefix(t, in) && in != t
	}
}

// HeadsUp reports whether "0: Dismiss" appears anywhere on screen, the
// pattern an earlier pane-messaging script used for a "Heads up" notice. UNVERIFIED: no
// real capture of the notice exists, and the text also matches ordinary
// conversation output. Use it only for messages, never to drive a keypress.
func HeadsUp(capture string) bool {
	return strings.Contains(capture, "0: Dismiss")
}

func trim(s string) string {
	return strings.TrimRight(strings.TrimLeft(s, "  "), "  ")
}
