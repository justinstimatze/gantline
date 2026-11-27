package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/justinstimatze/gantline/journal"
)

func TestRestartUsesCurrentModel(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := currentModel("k"); err == nil {
		t.Fatal("no snapshot: want an error, not a guessed model")
	}
	os.MkdirAll(journal.PaneDir("k"), 0o700)
	os.WriteFile(filepath.Join(journal.PaneDir("k"), "status.json"), []byte(`{"t":1,"model_id":"claude-opus-5-5"}`), 0o600)
	m, err := currentModel("k")
	if err != nil || m != "claude-opus-5-5" {
		t.Fatalf("currentModel = %q, %v", m, err)
	}
	got := restartArgs([]string{"--then", "x"}, m)
	if want := []string{"--then", "x", "claude-opus-5-5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restartArgs = %q, want %q", got, want)
	}
}

func TestFlagAfterText(t *testing.T) {
	if f := flagAfterText([]string{"/compact keep: x", "--then", "go on"}); f != "--then" {
		t.Fatalf("flag after the command: got %q, want --then", f)
	}
	if f := flagAfterText([]string{"/compact keep: the --thenable parts"}); f != "" {
		t.Fatalf("a word inside the command text was taken for a flag: %q", f)
	}
}

func TestTildePath(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	for in, want := range map[string]string{
		"/home/someone/.local/state/gantline/q.log": "~/.local/state/gantline/q.log",
		"/home/someoneelse/q.log":                   "/home/someoneelse/q.log",
		"/var/log/q.log":                            "/var/log/q.log",
	} {
		if got := tildePath(in); got != want {
			t.Errorf("tildePath(%q) = %q, want %q", in, got, want)
		}
	}
}
