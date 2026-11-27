package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/screen"
	"github.com/justinstimatze/gantline/tmux"
)

// maxLines caps default output to keep a self-read cheap (law 7).
const maxLines = 60

// screenCmd handles `gantline screen`: print what a pane shows, whole or one
// region, so a session can read its own screen. It never presses a key.
func screenCmd(s tmux.Server, args []string) {
	fs := flag.NewFlagSet("screen", flag.ExitOnError)
	target := fs.String("pane", "", "pane to read (default: this pane)")
	region := fs.String("region", "all", "region: "+strings.Join(screen.Regions, ", "))
	scroll := fs.Int("scrollback", 0, "also read this many lines of history above the screen")
	find := fs.String("find", "", "print only lines matching this regexp, with row numbers")
	since := fs.Bool("since", false, "print only lines that were not on this pane's previous `screen` read")
	keepANSI := fs.Bool("ansi", false, "keep terminal escapes (colour); regions are still cut on the plain text")
	all := fs.Bool("all", false, fmt.Sprintf("print every line (default: the last %d)", maxLines))
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gantline screen [--pane P] [--region R] [--scrollback N] [--find RE] [--since] [--ansi] [--all]")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	var re *regexp.Regexp
	if *find != "" {
		var err error
		if re, err = regexp.Compile(*find); err != nil {
			fmt.Fprintln(os.Stderr, "gantline screen: --find:", err)
			os.Exit(2)
		}
	}
	id, key, err := resolve(s, *target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gantline screen:", err)
		os.Exit(1)
	}
	plain, err := s.CaptureScroll(id, *scroll, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gantline screen:", err)
		os.Exit(1)
	}
	l := screen.Cut(plain)
	lines, row, ok := l.Region(*region)
	if !ok {
		fmt.Fprintf(os.Stderr, "gantline screen: unknown region %q (have %s)\n", *region, strings.Join(screen.Regions, ", "))
		os.Exit(2)
	}
	if *keepANSI {
		if colour, err := s.CaptureScroll(id, *scroll, true); err == nil {
			if c := strings.Split(colour, "\n"); len(c) >= row+len(lines) {
				lines = c[row : row+len(lines)]
			}
		}
	}

	prev := readFrame(key)
	writeFrame(key, l.Lines)
	numbered := re != nil || *since
	var out []string
	for i, line := range lines {
		plainLine := l.Lines[row+i]
		if re != nil && !re.MatchString(plainLine) {
			continue
		}
		if *since && prev[plainLine] > 0 {
			prev[plainLine]--
			continue
		}
		if numbered {
			line = fmt.Sprintf("%d: %s", row+i, line)
		} else if strings.TrimSpace(plainLine) == "" && len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			continue // one blank line stands for a run of them
		}
		out = append(out, line)
	}
	if !*all && len(out) > maxLines {
		fmt.Printf("(%d earlier lines not shown; --all prints them)\n", len(out)-maxLines)
		out = out[len(out)-maxLines:]
	}
	for _, line := range out {
		fmt.Println(line)
	}
}

// The previous frame is kept as line counts, so --since reports lines that
// are new even after the screen scrolled.
func framePath(key string) string { return filepath.Join(journal.PaneDir(key), "lastframe.json") }

func readFrame(key string) map[string]int {
	counts := map[string]int{}
	data, err := os.ReadFile(framePath(key))
	if err != nil {
		return counts
	}
	var lines []string
	json.Unmarshal(data, &lines)
	for _, l := range lines {
		counts[l]++
	}
	return counts
}

func writeFrame(key string, lines []string) {
	if err := os.MkdirAll(journal.PaneDir(key), 0o700); err != nil {
		return
	}
	data, _ := json.Marshal(lines)
	tmp := framePath(key) + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, framePath(key))
	}
}
