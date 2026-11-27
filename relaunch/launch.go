package relaunch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// Launch is what a relaunched pane runs: written to a 0600 file by the
// runner and read by `gantline launch`, so the session's environment (API
// keys among it) never sits on a tmux or ps command line.
type Launch struct {
	Dir  string   `json:"dir"`
	Env  []string `json:"env"`
	Argv []string `json:"argv"`
	// Shell: claude ran under a shell in the pane, which respawn-pane -k
	// kills. Run claude as a child and start a shell when it exits, so
	// the pane doesn't close with it.
	Shell bool `json:"shell,omitempty"`
}

// WriteLaunch writes l to a new 0600 file in dir and returns its path.
func WriteLaunch(dir string, l Launch) (string, error) {
	data, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "launch-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// RunLaunch reads and deletes the launch file at path, then runs it: exec
// claude in place, or with Shell, claude as a child and then the user's
// $SHELL. It returns only on error or, with Shell, if the shell can't run.
func RunLaunch(path string) error {
	data, err := os.ReadFile(path)
	os.Remove(path)
	if err != nil {
		return err
	}
	var l Launch
	if err := json.Unmarshal(data, &l); err != nil {
		return err
	}
	if len(l.Argv) == 0 {
		return errors.New("launch: empty command")
	}
	if err := os.Chdir(l.Dir); err != nil {
		return err
	}
	bin, err := exec.LookPath(l.Argv[0])
	if err != nil {
		bin = l.Argv[0]
	}
	if !l.Shell {
		return syscall.Exec(bin, l.Argv, l.Env)
	}
	// Ctrl-C reaches the whole foreground process group. Claude handles
	// it, so this process ignores it.
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT)
	cmd := exec.Command(bin, l.Argv[1:]...)
	cmd.Args[0] = l.Argv[0]
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = l.Env, os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			fmt.Fprintln(os.Stderr, "gantline launch:", err)
		}
	}
	signal.Reset(syscall.SIGINT, syscall.SIGQUIT)
	sh := "/bin/sh"
	for _, kv := range l.Env {
		if v, ok := strings.CutPrefix(kv, "SHELL="); ok && filepath.IsAbs(v) {
			sh = v
		}
	}
	return syscall.Exec(sh, []string{"-" + filepath.Base(sh)}, l.Env)
}

// Redact keeps a relaunch command line's shape for logs and the session's
// footer: the program, --resume and --model with their values, and every
// other flag's name, with values dropped (they can carry headers, JSON
// settings or system prompt text).
func Redact(argv []string) []string {
	out := make([]string, 0, len(argv))
	keep := false
	for i, a := range argv {
		switch {
		case i == 0, keep:
			out = append(out, a)
			keep = false
		case a == "--resume" || a == "-r" || a == "--model":
			out = append(out, a)
			keep = true
		case len(a) > 1 && a[0] == '-':
			name, _, _ := strings.Cut(a, "=")
			out = append(out, name)
		default:
			out = append(out, "…")
		}
	}
	return out
}
