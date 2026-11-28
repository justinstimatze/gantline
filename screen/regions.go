package screen

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Span is a half-open range of line indexes, [Start, End).
type Span struct{ Start, End int }

// Empty reports whether the span holds no lines.
func (s Span) Empty() bool { return s.End <= s.Start }

// Layout is a capture cut at Claude Code's layout landmarks: the "─" rules
// above and below the ❯ input line, the footer under the box, and a dialog
// drawn where the box would be. The cut uses only those landmarks, never
// known message text, so a screen no fingerprint covers still splits into
// regions.
type Layout struct {
	Lines   []string
	Box     Span // the input box, from the ❯ line to the rule under it
	Footer  Span // statusline and mode line, under the box
	Dialog  Span // a dialog drawn in place of the box: everything below the last rule
	Last    Span // the newest block above the box or dialog, from the last ❯ line
	Spinner int  // the in-progress spinner line ("✢ Puzzling… (12s)"), or -1

	// A box taller than the pane allows scrolls: its top rule reads
	// "─── ↑ 2 more ───" and the ❯ line is off screen (2.1.332 in a 16-row
	// pane), or its bottom rule reads "↓ N more" when the cursor is above.
	HiddenAbove, HiddenBelow bool
}

// Regions lists the names Region accepts.
var Regions = []string{"all", "box", "mode", "status", "last", "dialog", "spinner"}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-9A-B]`)

// StripANSI removes terminal escape sequences, including OSC 8 hyperlinks.
func StripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

// ghost is a dim run (SGR 2) up to the next reset: how Claude Code draws a
// prompt suggestion in the empty input box ("❯ read steps/3.txt"), which
// Tab accepts and the first typed key replaces.
var ghost = regexp.MustCompile(`\x1b\[2m[^\x1b]*(?:\x1b\[(?:0|22)?m)?`)

// Plain turns a capture taken with escapes into plain text, first deleting
// any dim run on a ❯ line, so a prompt suggestion reads as an empty box
// instead of a person's draft. Typed text and echoed prompts are drawn in
// colour, never dim (2.1.332 captures).
func Plain(capture string) string {
	lines := strings.Split(capture, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimLeft(StripANSI(l), " "), promptMarker) && strings.Contains(l, "\x1b[2m") {
			lines[i] = ghost.ReplaceAllString(l, "")
		}
	}
	return StripANSI(strings.Join(lines, "\n"))
}

// Cut splits a capture into regions. Trailing spaces, non-breaking spaces
// at line ends and trailing blank lines are dropped first.
func Cut(capture string) Layout {
	raw := strings.Split(strings.ReplaceAll(capture, " ", " "), "\n")
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	l := Layout{Lines: lines, Spinner: -1}
	n := len(lines)
	top := n // first line of the box's top rule, or of the dialog's rule

	// The box: a ❯ line with a rule directly above it and a rule below, or,
	// scrolled, any rows under a "↑ N more" rule.
	for i := n - 1; i > 0; i-- {
		scrolled := isScrollRule(lines[i-1], "↑") && !isRule(lines[i])
		if !scrolled && (!strings.HasPrefix(lines[i], promptMarker) || !isRule(lines[i-1])) {
			continue
		}
		j := i + 1
		for j < n && !isRule(lines[j]) {
			j++
		}
		if j == n {
			continue
		}
		// The footer (statusline, mode line) ends at the first blank line
		// after it: below it Claude Code may list background agents
		// ("● main"). A statusline that prints nothing leaves its row
		// blank, so blank lines straight under the rule are skipped first.
		k := j + 1
		for k < n && strings.TrimSpace(lines[k]) == "" {
			k++
		}
		for k < n && strings.TrimSpace(lines[k]) != "" {
			k++
		}
		l.Box, l.Footer, top = Span{i, j}, Span{j + 1, k}, i-1
		l.HiddenAbove, l.HiddenBelow = scrolled, isScrollRule(lines[j], "↓")
		break
	}
	if l.Box.Empty() {
		if b, ok := overflowBox(lines); ok {
			l.Box, l.Footer, top = b, Span{b.End + 1, n}, -1
			l.HiddenAbove = true
		}
	}
	if l.Box.Empty() {
		// No box: a dialog replaces it, drawn under one rule.
		for i := n - 1; i >= 0; i-- {
			if isRule(lines[i]) {
				l.Dialog, top = Span{i + 1, n}, i
				break
			}
		}
	}

	l.Last = Span{0, top}
	for i := top - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], promptMarker) {
			l.Last.Start = i
			break
		}
	}
	l.Last = l.trim(l.Last)
	for i := l.Last.End - 1; i >= l.Last.Start; i-- {
		if isSpinner(lines[i]) {
			l.Spinner = i
			break
		}
	}
	return l
}

// overflowBox finds an input box taller than the pane that Claude Code drew
// whole, so its top rule and ❯ line scrolled into tmux's history (2.1.332
// under tmux 3.8 on macOS, 56x16 pane). What shows is the box's tail: from
// the top row, only indented continuation lines, then the one rule on
// screen, then a footer ending in the mode line. A dialog draws its options
// under its rule, so its footer never reads as a mode line.
func overflowBox(lines []string) (Span, bool) {
	rule := -1
	for i, line := range lines {
		if isRule(line) {
			if rule >= 0 {
				return Span{}, false
			}
			rule = i
		}
	}
	if rule < 1 || rule == len(lines)-1 {
		return Span{}, false
	}
	for _, line := range lines[:rule] {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			return Span{}, false
		}
	}
	// The mode line's own hint, the only form in the local captures, not a
	// bare "shift+tab": a permission dialog's option reads "allow all edits
	// (shift+tab)". A notice may sit under it, so any footer row counts. A
	// mode line without the hint finds no box, so gantline waits.
	hint := false
	for _, line := range lines[rule+1:] {
		hint = hint || strings.Contains(line, "(shift+tab to cycle)")
	}
	if !hint {
		return Span{}, false
	}
	return Span{0, rule}, true
}

// modeLine is the footer row holding the mode line: the last one with a
// mode marker, else the last row. Claude Code can draw a notice under the
// mode line ("tmux focus-events off · add 'set -g focus-events on'…",
// macOS runner, tmux 3.8), which reading only the last row mistook for it.
func (l Layout) modeLine(f Span) int {
	for i := f.End - 1; i >= f.Start; i-- {
		if isModeLine(l.Lines[i]) {
			return i
		}
	}
	return f.End - 1
}

// isModeLine reports whether a footer row carries the mode line's markers,
// as drawn in the local captures and the macOS runner's.
func isModeLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "⏵⏵") || strings.HasPrefix(t, "⏸") ||
		strings.Contains(t, "(shift+tab to cycle)") || hasSegment(t, "esc to interrupt")
}

// trim narrows s past blank lines at either end.
func (l Layout) trim(s Span) Span {
	for s.Start < s.End && strings.TrimSpace(l.Lines[s.Start]) == "" {
		s.Start++
	}
	for s.End > s.Start && strings.TrimSpace(l.Lines[s.End-1]) == "" {
		s.End--
	}
	return s
}

// Region returns the lines of the named region and the row of its first
// line. An absent region returns no lines. ok is false for an unknown name.
func (l Layout) Region(name string) (lines []string, row int, ok bool) {
	var s Span
	switch name {
	case "all":
		s = Span{0, len(l.Lines)}
	case "box":
		s = l.Box
	case "dialog":
		s = l.Dialog
	case "last":
		s = l.Last
	case "mode", "status":
		f := l.trim(l.Footer)
		if f.Empty() {
			break
		}
		m := l.modeLine(f)
		if name == "mode" {
			s = Span{m, m + 1}
		} else {
			s = Span{f.Start, m}
		}
	case "spinner":
		if l.Spinner >= 0 {
			s = Span{l.Spinner, l.Spinner + 1}
		}
	default:
		return nil, 0, false
	}
	if s.Empty() {
		return nil, 0, true
	}
	return l.Lines[s.Start:s.End], s.Start, true
}

// Text returns the named region joined with newlines.
func (l Layout) Text(name string) string {
	lines, _, _ := l.Region(name)
	return strings.Join(lines, "\n")
}

// isRule reports whether line is a horizontal rule: at least 20 of "─"
// (around the input box and above a permission dialog) or "▔" (above the
// "Switch model?" confirmation). The box's top rule can carry the
// session's name near its right end ("──── demo ─"), so a "─" rule may
// hold a label as long as it starts and ends with "─". Dialogs draw inner
// separators with "╌", which is not a rule.
func isRule(line string) bool {
	t := strings.TrimSpace(line)
	if strings.Count(t, "─") < 20 && strings.Count(t, "▔") < 20 {
		return false
	}
	if strings.Trim(t, "▔") == "" {
		return true
	}
	return strings.HasPrefix(t, "─") && strings.HasSuffix(t, "─")
}

// isScrollRule reports whether line is a box rule carrying Claude Code's
// scroll mark, arrow ("↑" or "↓") then " N more".
func isScrollRule(line, arrow string) bool {
	if !isRule(line) {
		return false
	}
	_, after, ok := strings.Cut(line, arrow+" ")
	if !ok {
		return false
	}
	digits := strings.TrimLeft(after, "0123456789")
	return len(digits) < len(after) && strings.HasPrefix(digits, " more")
}

// isSpinner reports whether line is Claude Code's in-progress line: a
// glyph at column 0, a space, and an ellipsis ("* Compacting conversation…
// (5s)", "✢ Puzzling… (running UserPromptSubmit hooks… 19/20 · 12s)"). The
// finished form ("✻ Worked for 38s · done 10:07 PM") has no ellipsis.
func isSpinner(line string) bool {
	r, size := utf8.DecodeRuneInString(line)
	if size == 0 || unicode.IsSpace(r) || unicode.IsLetter(r) || unicode.IsDigit(r) {
		return false
	}
	switch r {
	case '❯', '●', '⎿', '─', '╌':
		return false
	}
	return strings.HasPrefix(line[size:], " ") && strings.Contains(line, "…")
}

// Option is one numbered choice in a dialog.
type Option struct {
	N        int
	Label    string
	Selected bool // the ❯ cursor is on it
}

var optionLine = regexp.MustCompile(`^\s*(❯\s+)?(\d+)\.\s+(.+)$`)

// Options parses the numbered choices out of dialog lines.
func Options(lines []string) []Option {
	var out []Option
	for _, line := range lines {
		m := optionLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n := 0
		for _, c := range m[2] {
			n = n*10 + int(c-'0')
		}
		out = append(out, Option{N: n, Label: strings.TrimSpace(m[3]), Selected: m[1] != ""})
	}
	return out
}
