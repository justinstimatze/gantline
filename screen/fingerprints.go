package screen

import (
	"strings"
	"unicode/utf8"
)

// Fingerprint recognises one screen state inside one region of a Layout.
//
// Law 6 (SPEC): gantline acts on a fingerprint (refuses to type, or waits)
// only if a real capture shows it. Entries with Acts set must list corpus
// files in Verified; TestFingerprintsVerified fails when they don't, or when
// a listed capture stops matching. Globs are relative to the version
// directory of the local corpus (~/.local/state/gantline/corpus/<version>/),
// which never enters git.
type Fingerprint struct {
	ID       string
	Region   string // a name from Regions
	Acts     bool   // say/q wait or refuse on it
	Verified []string
	Never    []string // corpus globs that must not match
	Match    func(lines []string) bool
}

// CheckedVersions are the Claude Code versions whose screens the
// fingerprints below were verified against. Add one only after the corpus
// and e2e tests pass on it.
var CheckedVersions = []string{"2.1.332"}

// Fingerprints is every screen state gantline recognises, for Claude Code
// 2.1.332.
var Fingerprints = []Fingerprint{
	{
		ID: "busy", Region: "mode", Acts: true,
		// The mode line gains an "esc to interrupt" segment while a turn,
		// a compaction or the prompt hooks run. With a background shell the
		// segment is not last: "⏵⏵ auto mode on · 1 shell · esc to interrupt · ↓ to manage".
		Verified: []string{"busy-running-*.txt", "frames/perm-*.txt", "frames/compact-00*.txt", "live/agent-list-*.txt", "live/narrow-busy-*.txt"},
		Never:    []string{"idle-*.txt", "frames/outcome_*.txt", "frames/interrupted-idle.txt", "frames/compact-1*.txt"},
		Match: func(lines []string) bool {
			for _, l := range lines {
				if hasSegment(l, "esc to interrupt") {
					return true
				}
			}
			return false
		},
	},
	{
		ID: "permission_dialog", Region: "dialog", Acts: true,
		Verified: []string{"frames/permdlg-*.txt"},
		Never:    []string{"idle-*.txt", "busy-*.txt", "trust-folder-*.txt", "frames/perm-*.txt", "frames/compact-*.txt", "frames/outcome_*.txt"},
		Match: func(lines []string) bool {
			return hasLine(lines, "Do you want to proceed?") && len(Options(lines)) > 0
		},
	},
	{
		ID: "trust_folder", Region: "dialog", Acts: true,
		Verified: []string{"trust-folder-*.txt"},
		Never:    []string{"idle-*.txt", "frames/permdlg-*.txt", "frames/compact-*.txt"},
		Match: func(lines []string) bool {
			return hasLine(lines, "Yes, I trust this folder")
		},
	},
	{
		// /model mid-conversation: "Switch model? Your next response will be
		// slower…", drawn under a "▔" rule, options "1. Yes, switch to X" /
		// "2. No, go back".
		ID: "model_switch_confirm", Region: "dialog", Acts: true,
		Verified: []string{"e2e/fail-1795591182.txt"},
		Never:    []string{"idle-*.txt", "frames/permdlg-*.txt", "trust-folder-*.txt", "frames/outcome_*.txt"},
		Match: func(lines []string) bool {
			return hasLine(lines, "Switch model?") && len(Options(lines)) > 0
		},
	},
	{
		// Informational: the journal's PreCompact/PostCompact are the source
		// of truth for compaction; this only labels the spinner.
		ID: "compacting", Region: "spinner",
		Verified: []string{"frames/compact-00[2-8].txt"},
		Never:    []string{"frames/perm-*.txt", "idle-*.txt"},
		Match: func(lines []string) bool {
			return len(lines) > 0 && strings.Contains(lines[0], "Compacting conversation")
		},
	},
}

// Match returns the ids of the fingerprints that match l, in table order.
func Match(l Layout) []string {
	var ids []string
	for _, f := range Fingerprints {
		if lines, _, _ := l.Region(f.Region); len(lines) > 0 && f.Match(lines) {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

// hasSegment reports whether line, split on " · ", has a segment equal to seg.
// hasSegment reports whether line has the "·"-separated segment seg. A
// pane narrower than the mode line cuts its last segment with "…" ("· esc…"
// in a 56-column pane), so a final segment of at least three runes that is
// a prefix of seg followed by "…" counts too.
func hasSegment(line, seg string) bool {
	parts := strings.Split(line, "·")
	for i, s := range parts {
		s = strings.TrimSpace(s)
		if s == seg {
			return true
		}
		if i == len(parts)-1 {
			if cut, ok := strings.CutSuffix(s, "…"); ok && utf8.RuneCountInString(cut) >= 3 && strings.HasPrefix(seg, cut) {
				return true
			}
		}
	}
	return false
}

func hasLine(lines []string, text string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) == text {
			return true
		}
	}
	return false
}

// Dialog returns the id of the recognised dialog on screen, or "" if none.
// A dialog region no fingerprint recognises returns "".
func Dialog(l Layout) string {
	lines, _, _ := l.Region("dialog")
	if len(lines) == 0 {
		return ""
	}
	for _, f := range Fingerprints {
		if f.Region == "dialog" && f.Match(lines) {
			return f.ID
		}
	}
	return ""
}

// DialogTitle returns the first non-blank line of the dialog region, or "".
func DialogTitle(l Layout) string {
	lines, _, _ := l.Region("dialog")
	for _, line := range lines {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
