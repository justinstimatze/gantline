package relaunch

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const sid = "00000000-0000-4000-8000-000000000002"

func TestArgv(t *testing.T) {
	tail := []string{"--resume", "NEW", "--model", "sonnet"}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "claude", "claude"},
		{"resume with launch prompt", "claude --dangerously-skip-permissions --resume " + sid + " resumed;_re-arm_your_watches",
			"claude --dangerously-skip-permissions"},
		{"old model and inline forms", "claude --model haiku --permission-mode=acceptEdits --model=opus -n demo", "claude --permission-mode=acceptEdits -n demo"},
		{"variadic keeps values", "claude --add-dir /a /b --verbose", "claude --add-dir /a /b --verbose"},
		{"optional with and without value", "claude --remote-control box --debug --verbose", "claude --remote-control box --debug --verbose"},
		{"continue and fork", "claude -c --fork-session --session-id " + sid, "claude"},
		{"bare resume then prompt", "claude -r hello there", "claude"},
		{"double dash", "claude --verbose -- -not-a-flag", "claude --verbose"},
	}
	for _, c := range cases {
		got, err := Argv(strings.Fields(c.in), "NEW", "sonnet")
		want := append(strings.Fields(c.want), tail...)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Argv = %q, %v; want %q", c.name, got, err, want)
		}
	}
}

func TestArgvRefuses(t *testing.T) {
	for _, in := range []string{"claude -p hi", "claude --print", "claude --frobnicate", "claude --model", "claude --bg", "claude -w feature"} {
		if _, err := Argv(strings.Fields(in), "NEW", "sonnet"); !errors.Is(err, ErrArgv) {
			t.Errorf("%q: err = %v, want ErrArgv", in, err)
		}
	}
	for _, m := range []string{"", "--print", "a b"} {
		if _, err := Argv([]string{"claude"}, "NEW", m); !errors.Is(err, ErrArgv) {
			t.Errorf("model %q accepted", m)
		}
	}
}
