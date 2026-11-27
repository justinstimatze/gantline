package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/justinstimatze/gantline/internal/audit"
	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/queue"
	"github.com/justinstimatze/gantline/relaunch"
	"github.com/justinstimatze/gantline/screen"
	"github.com/justinstimatze/gantline/tmux"
)

// queueCmd handles `gantline q [--pane P] [--then TEXT] [--timeout D] <text>`:
// add text to the pane's queue and make sure a runner is working it. The
// runner is a detached copy of gantline (`q --run`), so this returns at
// once and the caller can end its turn.
func queueCmd(s tmux.Server, args []string) {
	fs := flag.NewFlagSet("q", flag.ExitOnError)
	target := fs.String("pane", "", "pane to type into (default: this pane)")
	then := fs.String("then", "", "text to type after the command is confirmed done (e.g. a prompt to resume work)")
	timeout := fs.Duration("timeout", 30*time.Minute, "give up if the pane isn't idle within this long")
	run := fs.Bool("run", false, "internal: run the pane's queue")
	show := fs.String("show", "", "print the full output of queued item ID (e.g. q1) from the transcript")
	showScreen := fs.Bool("screen", false, "with --show: print what the pane showed above the box when the command finished, not the transcript copy")
	unchecked := fs.Bool("unchecked", false, "queue a slash command gantline hasn't checked; if it opens a dialog, the session waits for a person")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gantline q [--pane P] [--then TEXT] [--timeout D] [--unchecked] <text> | gantline q [--pane P] --show <id> [--screen]")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if f := flagAfterText(fs.Args()); f != "" {
		fmt.Fprintf(os.Stderr, "gantline q: %s comes after the command text, so it would be typed as part of it; put flags first: gantline q %s ... '<command>'\n", f, f)
		os.Exit(2)
	}
	id, key, err := resolve(s, *target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gantline q:", err)
		os.Exit(1)
	}
	logf, err := os.OpenFile(filepath.Join(audit.StateDir(), "q.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gantline q:", err)
		os.Exit(1)
	}
	if *run {
		err := queue.Run(s, id, key, queue.RunOptions{Log: func(format string, a ...any) {
			fmt.Fprintf(logf, "%s q %s: %s\n", time.Now().Format(time.RFC3339), id, fmt.Sprintf(format, a...))
		}})
		if err != nil {
			fmt.Fprintf(logf, "%s q %s: runner: %v\n", time.Now().Format(time.RFC3339), id, err)
			os.Exit(1)
		}
		return
	}

	if *show != "" {
		get := queue.Output
		if *showScreen {
			get = queue.ScreenOutput
		}
		out, err := get(key, *show)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gantline q:", err)
			os.Exit(1)
		}
		fmt.Println(out)
		return
	}
	text := strings.Join(fs.Args(), " ")
	if text == "" {
		fs.Usage()
		os.Exit(2)
	}
	if err := queue.Guard(text, *unchecked); err != nil {
		fmt.Fprintln(os.Stderr, "gantline q:", err)
		os.Exit(2)
	}
	it, err := queue.AddUnchecked(key, text, *then, *timeout, idleNow(s, id, key), time.Now(), *unchecked)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gantline q:", err)
		os.Exit(1)
	}
	startRunner(id, key, it, logf)
	fmt.Printf("gantline q: %s queued for %s; typed once the turn ends and checked. Outcome: `gantline status` or %s\n",
		it.ID, id, tildePath(logf.Name()))
}

// startRunner starts a detached runner for the pane (`q --run`); it exits
// at once if one is already working the queue. Detached (setsid), it
// outlives the claude process that queued the item, which a relaunch kills.
func startRunner(id, key string, it queue.Item, logf *os.File) {
	self, err := os.Executable()
	if err == nil {
		cmd := exec.Command(self, "q", "--run", "--pane", id)
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		err = cmd.Start()
	}
	if err != nil {
		queue.Set(key, it.ID, queue.Failed, "could not start the runner: "+err.Error())
		fmt.Fprintln(os.Stderr, "gantline:", err)
		os.Exit(1)
	}
}

// rearm is model's default follow-up: a relaunch ends everything the old
// process ran, and nothing else tells the session so.
const rearm = "gantline relaunched this session on a new model. Background tasks, Monitor watches, /loop schedules and message waits from before are gone: re-arm the ones you had running, read any messages that arrived meanwhile, then carry on from where you left off."

// modelCmd handles `gantline model [--pane P] [--then TEXT] [--timeout D]
// <model>`: queue a relaunch of the pane's session on model, which leaves
// the default model in ~/.claude/settings.json alone (`/model` rewrites it).
func modelCmd(s tmux.Server, args []string) { modelOrRestart(s, "model", args) }

// modelOrRestart queues a relaunch; name is the subcommand the caller ran,
// for its messages.
func modelOrRestart(s tmux.Server, name string, args []string) {
	prefix := "gantline " + name + ":"
	fs := flag.NewFlagSet("model", flag.ExitOnError)
	target := fs.String("pane", "", "pane whose session to relaunch (default: this pane)")
	then := fs.String("then", rearm, "text to type once the session is back on the new model; '' for none")
	timeout := fs.Duration("timeout", 30*time.Minute, "give up if the pane isn't idle within this long")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gantline model [--pane P] [--then TEXT] [--timeout D] <model>")
		fmt.Fprintln(os.Stderr, "Relaunches the session in place (claude --resume <id> --model <model>, same flags) once the turn ends.")
		fmt.Fprintln(os.Stderr, "Background tasks, watches and /loop schedules end with the old process; the default --then asks the session to re-arm them.")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	id, key, err := resolve(s, *target)
	if err != nil {
		fmt.Fprintln(os.Stderr, prefix, err)
		os.Exit(1)
	}
	logf, err := os.OpenFile(filepath.Join(audit.StateDir(), "q.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, prefix, err)
		os.Exit(1)
	}
	// Check now that the session can be relaunched, so a refusal (print
	// mode, an unknown flag) reaches the caller instead of the queue log.
	out, err := s.Run("display-message", "-p", "-t", id, "#{pane_pid}")
	if err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(out))
		var sess relaunch.Session
		if sess, err = relaunch.Find(pid); err == nil {
			_, err = relaunch.Argv(sess.Argv, sess.ID, fs.Arg(0))
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, prefix, err)
		os.Exit(1)
	}
	it, err := queue.AddModel(key, fs.Arg(0), *then, *timeout, idleNow(s, id, key), time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, prefix, err)
		os.Exit(2)
	}
	startRunner(id, key, it, logf)
	fmt.Printf("%s %s queued for %s; the session restarts on %s once the turn ends. Outcome: `gantline status` or %s\n",
		prefix, it.ID, id, fs.Arg(0), tildePath(logf.Name()))
}

// idleNow reports whether the pane is idle right now: the screen shows its
// box and no busy line, and the hooks don't say a turn is running. Text
// queued from inside a turn sees a busy screen, so it waits for the Stop.
func idleNow(s tmux.Server, id, key string) bool {
	c, err := s.Capture(id)
	if err != nil {
		return false
	}
	if _, box := screen.Input(c); !box || screen.Busy(c) {
		return false
	}
	st, _, _ := journal.Load(key)
	return st.Activity != journal.Working && st.Activity != journal.Compacting
}

// flagAfterText returns the first of q's own flags found among the
// positional arguments. Go's flag package stops at the first non-flag, so
// `gantline q '/compact' --then x` would otherwise queue "--then x" as
// part of the command.
func flagAfterText(args []string) string {
	for _, a := range args {
		for _, f := range []string{"--then", "-then", "--pane", "-pane", "--timeout", "-timeout", "--show", "-show", "--screen", "-screen"} {
			if a == f || strings.HasPrefix(a, f+"=") {
				return f
			}
		}
	}
	return ""
}

// tildePath writes a path under the home directory as ~/..., so the message
// a session prints into its own transcript and screen doesn't carry the
// user name.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return p
	}
	if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return p
}
