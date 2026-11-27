// Command gantline reads and drives Claude Code panes in tmux.
//
//	gantline say <pane|self> <message>   submit message and confirm it went in
//	gantline q [--pane P] [--then T] <text>   type text into P (default: this pane) after the turn ends, then check it
//	gantline status [--pane P] [--json|--raw]   what a pane is doing: hooks plus screen
//	gantline screen [--pane P] [--region R] ...  print what a pane shows (self-read)
//	gantline panes                       list panes running claude
//	gantline install|uninstall [--dry-run]   add or remove gantline's hooks and statusline wrapper in ~/.claude/settings.json
//	gantline hook [Event]                hook entry point: append the event to this pane's journal
//	gantline statusline [-- cmd...]      statusline wrapper: snapshot model and context %, run cmd
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"time"

	"github.com/justinstimatze/gantline/internal/audit"
	"github.com/justinstimatze/gantline/pane"
	"github.com/justinstimatze/gantline/relaunch"
	"github.com/justinstimatze/gantline/say"
	"github.com/justinstimatze/gantline/tmux"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	s := tmux.FromEnv()
	switch os.Args[1] {
	case "-h", "--help", "help":
		help()
	case "version", "--version":
		fmt.Println("gantline", buildVersion())
	case "say":
		if len(os.Args) != 4 {
			usage()
		}
		target, msg := os.Args[2], os.Args[3]
		start := time.Now()
		opt := say.Options{}
		// Never fall back to the raw target: tmux reads an empty or stale
		// -t as "the most recently active pane", which may be someone else's.
		id, key, err := resolve(s, target)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gantline say:", err)
			os.Exit(1)
		}
		target, opt.Lock = id, key
		n, err := say.Submit(s, target, msg, opt)
		audit.Write(audit.Rec{Kind: "say", Pane: target, Bytes: len(msg), Enters: n, Took: time.Since(start), Err: err})
		if err != nil {
			fmt.Fprintf(os.Stderr, "gantline say: %s: %v\n", target, err)
			if errors.Is(err, say.ErrDraft) || errors.Is(err, say.ErrNoInputBox) || errors.Is(err, say.ErrDialog) {
				os.Exit(3)
			}
			os.Exit(1)
		}
		fmt.Printf("gantline say: submitted to %s (Enter presses: %d)\n", target, n)
	case "q":
		queueCmd(s, os.Args[2:])
	case "model":
		modelCmd(s, os.Args[2:])
	case "restart":
		restartCmd(s, os.Args[2:])
	case "audit":
		auditCmd(os.Args[2:])
	case "status":
		statusCmd(s, os.Args[2:])
	case "screen":
		screenCmd(s, os.Args[2:])
	case "install":
		installCmd(os.Args[2:], false)
	case "uninstall":
		installCmd(os.Args[2:], true)
	case "hook":
		hook(os.Args[2:])
	case "statusline":
		statusline(os.Args[2:])
	case "launch": // internal: what respawn-pane runs for a relaunch
		if len(os.Args) != 3 {
			usage()
		}
		err := relaunch.RunLaunch(os.Args[2])
		fmt.Fprintln(os.Stderr, "gantline launch:", err)
		os.Exit(1)
	case "panes":
		panes, err := pane.List(s)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gantline panes:", err)
			os.Exit(1)
		}
		for _, p := range panes {
			fmt.Printf("%s\t%d\t%s\n", p.ID, p.ClaudePID, p.Cwd)
		}
	default:
		usage()
	}
}

const usageText = `usage: gantline <command> [flags]

Read yourself:
  status  [--pane P] [--json|--raw]          activity, model, context, input box, dialog, queue
  screen  [--pane P] [--region R] [--find RE] [--scrollback N] [--since] [--all]

Control yourself (flags go before the command text):
  q       [--pane P] [--then TEXT] [--timeout D] '<slash command>'
  q       [--pane P] --show <id> [--screen]  what a queued command printed
  restart [--pane P] [--then TEXT] [--timeout D]
  model   [--pane P] [--then TEXT] [--timeout D] <model>

Other panes:
  panes                                      tmux panes running Claude Code
  say     <pane|self> '<text>'               type and submit, with no turn or draft guard

Setup and checks:
  install|uninstall [--settings F] [--dry-run]
  audit   [--since D] [--e2e]                deliveries made during a running turn; exit 1 if any
  version

Internal, run by Claude Code or tmux: hook, statusline, launch.
Run gantline <command> -h for a command's flags.
`

func usage() {
	fmt.Fprint(os.Stderr, usageText)
	os.Exit(2)
}

func help() {
	fmt.Print(usageText)
	os.Exit(0)
}

// version is set at release with -ldflags "-X main.version=vX.Y.Z".
var version = ""

// buildVersion is the release version, else the module version go install
// recorded, else the VCS revision.
func buildVersion() string {
	if version != "" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	rev, dirty := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev == "" {
		return "devel"
	}
	if dirty {
		rev += "+dirty"
	}
	return "devel-" + rev
}
