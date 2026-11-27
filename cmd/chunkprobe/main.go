// Command chunkprobe measures which send-keys chunk sizes and gaps deliver
// text intact into a real Claude Code input box. It types into the input box
// and clears it again; it never presses Enter, so no prompt is submitted.
//
//	chunkprobe -socket gl-chunk -pane %0 -reps 5
//
// The target pane must show an idle Claude Code prompt.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/justinstimatze/gantline/tmux"
)

type config struct {
	chunk int
	gap   time.Duration
}

func main() {
	socket := flag.String("socket", "", "tmux -L socket name")
	paneID := flag.String("pane", "", "target pane id")
	reps := flag.Int("reps", 5, "runs per config")
	length := flag.Int("len", 300, "text length in bytes (multiple of 5)")
	only := flag.String("configs", "", "comma-separated chunk:gapms pairs to run instead of the defaults")
	flag.Parse()
	s := tmux.Server{Socket: *socket}

	var b strings.Builder
	for i := 0; b.Len() < *length; i++ {
		fmt.Fprintf(&b, "m%03d.", i)
	}
	text := b.String()[:*length]

	configs := []config{
		{len(text), time.Millisecond}, // one burst
		{100, 150 * time.Millisecond},
		{40, 150 * time.Millisecond},
		{40, 20 * time.Millisecond},
		{40, time.Millisecond},
	}
	if *only != "" {
		configs = nil
		for _, pair := range strings.Split(*only, ",") {
			var c, g int
			if _, err := fmt.Sscanf(pair, "%d:%d", &c, &g); err != nil {
				fmt.Fprintln(os.Stderr, "bad -configs entry:", pair)
				os.Exit(2)
			}
			configs = append(configs, config{c, time.Duration(g) * time.Millisecond})
		}
	}
	type result struct {
		ok, n   int
		samples []string
	}
	results := make([]result, len(configs))

	for r := 0; r < *reps; r++ {
		for i, c := range configs { // interleaved: every config once per rep
			if err := clear(s, *paneID); err != nil {
				fmt.Fprintln(os.Stderr, "clear:", err)
				os.Exit(1)
			}
			start := time.Now()
			if err := s.Type(*paneID, text, tmux.TypeOptions{ChunkBytes: c.chunk, Gap: c.gap}); err != nil {
				fmt.Fprintln(os.Stderr, "type:", err)
				os.Exit(1)
			}
			took := time.Since(start)
			time.Sleep(700 * time.Millisecond)
			got := inputLine(s, *paneID)
			results[i].n++
			if got == text {
				results[i].ok++
			} else if len(results[i].samples) < 2 {
				results[i].samples = append(results[i].samples, fmt.Sprintf("len=%d %q", len(got), trunc(got)))
			}
			fmt.Printf("rep %d chunk=%d gap=%v typed in %v intact=%v\n", r, c.chunk, c.gap, took.Round(time.Millisecond), got == text)
		}
	}
	clear(s, *paneID)
	fmt.Println("---")
	for i, c := range configs {
		fmt.Printf("chunk=%-4d gap=%-6v intact %d/%d\n", c.chunk, c.gap, results[i].ok, results[i].n)
		for _, smp := range results[i].samples {
			fmt.Println("   got", smp)
		}
	}
}

// inputLine returns the text after the "❯" prompt marker on the input line.
func inputLine(s tmux.Server, pane string) string {
	screen, err := s.Capture(pane)
	if err != nil {
		return "ERR " + err.Error()
	}
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		// Claude Code 2.1.332 draws the marker as "❯" plus U+00A0. A long
		// input wraps onto following lines until the box's bottom rule.
		rest, ok := strings.CutPrefix(lines[i], "❯")
		if !ok {
			continue
		}
		parts := []string{strings.TrimLeft(rest, " \u00a0")}
		for _, l := range lines[i+1:] {
			if strings.HasPrefix(l, "─") {
				break
			}
			parts = append(parts, strings.TrimLeft(l, " \u00a0"))
		}
		for j := range parts {
			parts[j] = strings.TrimRight(parts[j], " ")
		}
		return strings.Join(parts, "")
	}
	return "NO PROMPT LINE"
}

// clear empties the input box with C-u until the placeholder or an empty
// prompt line shows.
func clear(s tmux.Server, pane string) error {
	for range 60 {
		if _, err := s.Run("send-keys", "-t", pane, "C-u"); err != nil {
			return err
		}
		time.Sleep(300 * time.Millisecond)
		got := inputLine(s, pane)
		if got == "" || strings.HasPrefix(got, "Try \"") {
			return nil
		}
	}
	return fmt.Errorf("input box did not clear: %q", trunc(inputLine(s, pane)))
}

func trunc(s string) string {
	if len(s) > 120 {
		return s[:60] + "…" + s[len(s)-60:]
	}
	return s
}
