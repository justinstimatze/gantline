package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/queue"
)

// hook handles `gantline hook [Event]`: append the hook's stdin payload,
// reduced to its whitelisted fields, to this pane's journal. It always
// exits 0, so it can never block a session. Outside tmux it does nothing.
// The event name comes from the payload; the argument is a fallback, and
// the old `gantline hook stop` form still works.
//
// It prints only for two events, and only when there is news from the
// pane's queue (law 10). On UserPromptSubmit: one line per queued command
// whose outcome the agent hasn't seen. On SessionStart after a compaction:
// that a follow-up is still to come. Claude Code adds that stdout to the
// model's context when the hook runs synchronously.
func hook(args []string) {
	in, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	key := journal.KeyFromEnv()
	if key == "" {
		return
	}
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	ev, err := journal.FromHook(in, name, time.Now())
	if err != nil {
		return
	}
	journal.Append(key, ev)
	switch {
	case ev.Ev == "UserPromptSubmit":
		items, _ := queue.Items(key)
		lines, ids := queue.Footer(items, 5)
		for i, l := range lines {
			fmt.Println(l)
			queue.MarkReported(key, ids[i])
		}
	case ev.Ev == "SessionStart" && ev.Source == "compact":
		items, _ := queue.Items(key)
		for _, it := range items {
			if it.State == queue.Running && it.Then != "" && queue.Kind(it.Cmd) == "compact" {
				fmt.Printf("gantline: %s compacted this session; its follow-up message is queued and will be typed when the compaction settles.\n", it.ID)
			}
		}
	}
}

// statusline handles `gantline statusline [-- cmd args...]`: keep this
// pane's latest statusline snapshot, then run the wrapped statusline
// command with the same stdin so the visible status line doesn't change.
func statusline(args []string) {
	in, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if key := journal.KeyFromEnv(); key != "" {
		if st, err := journal.StatusFromStatusline(in, time.Now()); err == nil {
			journal.WriteStatus(key, st)
		}
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		os.Exit(1)
	}
}
