package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/justinstimatze/gantline/journal"
	"github.com/justinstimatze/gantline/tmux"
)

// restartCmd handles `gantline restart [--pane P] [--then TEXT] [--timeout D]`:
// a relaunch in place on the model the session runs now, which reloads
// settings, hooks and MCP servers without changing the conversation.
func restartCmd(s tmux.Server, args []string) {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			fmt.Fprintln(os.Stderr, "usage: gantline restart [--pane P] [--then TEXT] [--timeout D]")
			fmt.Fprintln(os.Stderr, "Relaunches the session in place on the model it runs now, once the turn ends: same conversation and flags; settings, hooks and MCP servers load fresh.")
			fmt.Fprintln(os.Stderr, "Takes the same flags as `gantline model`; see `gantline model -h`.")
			os.Exit(0)
		}
	}
	pane := ""
	for i, a := range args {
		switch {
		case a == "--pane" && i+1 < len(args):
			pane = args[i+1]
		case strings.HasPrefix(a, "--pane="):
			pane = strings.TrimPrefix(a, "--pane=")
		}
	}
	_, key, err := resolve(s, pane)
	if err == nil {
		var model string
		if model, err = currentModel(key); err == nil {
			modelOrRestart(s, "restart", restartArgs(args, model))
			return
		}
	}
	fmt.Fprintln(os.Stderr, "gantline restart:", err)
	os.Exit(1)
}

// restartArgs is the `gantline model` argument list for a restart on model.
func restartArgs(args []string, model string) []string {
	return append(append([]string{}, args...), model)
}

// currentModel is the model id the pane's statusline last reported.
func currentModel(key string) (string, error) {
	st, err := journal.ReadStatus(key)
	if err != nil {
		return "", fmt.Errorf("no statusline snapshot for this pane (is `gantline install` applied?): %w", err)
	}
	if st.ModelID == "" {
		return "", errors.New("the statusline snapshot names no model")
	}
	return st.ModelID, nil
}
