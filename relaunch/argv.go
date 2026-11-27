// Package relaunch restarts a pane's Claude Code session in place with a
// different model: the same session (--resume <id>), the same flags, the
// same directory and environment, and --model set for this process only.
//
// `/model X` typed into a session also saves X as the default for every new
// session (~/.claude/settings.json), and Claude Code has no per-session
// form of it. `claude --resume <id> --model X` changes only that process.
// The old process can't start its successor, so the queue runner (a
// detached process) does it with `tmux respawn-pane -k`, which keeps the
// pane id and so the pane's journal key.
package relaunch

import (
	"errors"
	"fmt"
	"strings"
)

// valueKind is how a claude flag takes its value (claude --help, 2.1.332).
type valueKind int

const (
	none     valueKind = iota
	required           // <v>: always the next argument
	optional           // [v]: the next argument unless it starts with "-"
	variadic           // <v...>: every argument up to the next one starting with "-"
)

// flags is every flag `claude --help` lists for 2.1.332. A flag not in it
// is refused: guessing whether it takes a value could swallow or leak an
// argument.
var flags = map[string]valueKind{
	"--add-dir": variadic, "--agent": required, "--agents": required,
	"--allow-dangerously-skip-permissions": none,
	"--allowedTools":                       variadic, "--allowed-tools": variadic,
	"--append-system-prompt": required, "--append-system-prompt-file": required,
	"--autocompact": required, "--ax-screen-reader": none,
	"--bg": none, "--background": none, "--bare": none, "--betas": variadic, "--brief": none,
	"--chrome": none, "--cloud": optional, "-c": none, "--continue": none,
	"--dangerously-skip-permissions": none, "-d": optional, "--debug": optional,
	"--debug-file": required, "--desktop": none, "--disable-slash-commands": none,
	"--disallowedTools": variadic, "--disallowed-tools": variadic,
	"--effort": required, "--environment": required,
	"--exclude-dynamic-system-prompt-sections": none,
	"--fallback-model":                         required, "--file": variadic, "--fork-session": none,
	"--forward-subagent-text": none, "--from-pr": optional, "-h": none, "--help": none,
	"--ide": none, "--include-hook-events": none, "--include-partial-messages": none,
	"--input-format": required, "--json-schema": required, "--max-budget-usd": required,
	"--mcp-config": variadic, "--model": required, "-n": required, "--name": required,
	"--no-chrome": none, "--no-session-persistence": none, "--output-format": required,
	"--permission-mode": required, "--permission-prompts": required,
	"--plugin-dir": required, "--plugin-url": required, "-p": none, "--print": none,
	"--prompt-suggestions": optional, "--remote-control": optional,
	"--remote-control-session-name-prefix": required, "--replay-user-messages": none,
	"--restricted": none, "-r": optional, "--resume": optional, "--safe-mode": none,
	"--session-id": required, "--setting-sources": required, "--settings": required,
	"--strict-mcp-config": none, "--system-prompt": required, "--system-prompt-file": required,
	"--system-prompt-snapshot": required, "--teleport": optional, "--tmux": none,
	"--tools": variadic, "--verbose": none, "-v": none, "--version": none,
	"-w": optional, "--worktree": optional,
}

// dropped flags are replaced by the relaunch's own --resume and --model, or
// pick a session some other way.
var dropped = map[string]bool{
	"--model": true, "-r": true, "--resume": true, "-c": true, "--continue": true,
	"--session-id": true, "--fork-session": true,
}

// refused flags mean the process is not an interactive session that
// --resume can bring back in place.
var refused = map[string]string{
	"-p": "print mode", "--print": "print mode", "--bg": "a background session",
	"--background": "a background session", "--cloud": "a cloud session",
	"--teleport": "a teleported session", "--desktop": "a desktop handoff",
	"--no-session-persistence": "nothing on disk to resume", "-w": "a worktree launch",
	"--worktree": "a worktree launch", "--from-pr": "a PR launch",
	"-h": "help", "--help": "help", "-v": "version", "--version": "version",
}

// ErrArgv means the original command line can't be safely rewritten.
var ErrArgv = errors.New("relaunch: can't rewrite the session's command line")

// Argv rewrites a claude command line (argv[0] first) to resume session sid
// with model. It keeps every other flag and its values in order, drops the
// flags that choose a session or a model, and drops positional arguments:
// a launch prompt (some session managers pass one) would otherwise be sent
// again.
func Argv(argv []string, sid, model string) ([]string, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("%w: empty", ErrArgv)
	}
	if sid == "" || model == "" || strings.HasPrefix(model, "-") || strings.ContainsAny(model, " \t\n") {
		return nil, fmt.Errorf("%w: bad session %q or model %q", ErrArgv, sid, model)
	}
	out := []string{argv[0]}
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			break // only positional arguments follow
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			continue // positional: a launch prompt
		}
		name, _, inline := strings.Cut(a, "=")
		kind, known := flags[name]
		if !known {
			return nil, fmt.Errorf("%w: unknown flag %q", ErrArgv, name)
		}
		if why, bad := refused[name]; bad {
			return nil, fmt.Errorf("%w: %s (%s)", ErrArgv, name, why)
		}
		vals := []string{}
		if !inline {
			switch kind {
			case required:
				if i+1 >= len(argv) {
					return nil, fmt.Errorf("%w: %s has no value", ErrArgv, name)
				}
				i++
				vals = append(vals, argv[i])
			case optional:
				if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") && !(name == "-r" || name == "--resume") {
					i++
					vals = append(vals, argv[i])
				} else if (name == "-r" || name == "--resume") && i+1 < len(argv) && looksLikeID(argv[i+1]) {
					i++ // the session id; a prompt after a bare --resume stays positional
				}
			case variadic:
				for i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
					i++
					vals = append(vals, argv[i])
				}
			}
		}
		if dropped[name] {
			continue
		}
		out = append(out, a)
		out = append(out, vals...)
	}
	return append(out, "--resume", sid, "--model", model), nil
}

// looksLikeID reports whether s is a session id (a UUID) rather than a
// launch prompt following a bare --resume.
func looksLikeID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
	}
	return true
}
