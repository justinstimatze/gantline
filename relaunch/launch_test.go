package relaunch

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	in := []string{"claude", "--resume", "sid-1", "--model", "sonnet", "--append-system-prompt", "secret text", "--settings={\"a\":1}", "-c"}
	want := []string{"claude", "--resume", "sid-1", "--model", "sonnet", "--append-system-prompt", "…", "--settings", "-c"}
	if got := Redact(in); !reflect.DeepEqual(got, want) {
		t.Errorf("Redact = %q\nwant     %q", got, want)
	}
}

// TestRunLaunchShell runs RunLaunch in a child process: with Shell set, it
// runs the command, then execs $SHELL, and deletes the launch file.
func TestRunLaunchShell(t *testing.T) {
	if path := os.Getenv("GANTLINE_TEST_LAUNCH"); path != "" {
		t.Fatal(RunLaunch(path))
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	shell := filepath.Join(dir, "fakeshell")
	os.WriteFile(shell, []byte("#!/bin/sh\necho shell \"$0\" \"$SECRET\" >> "+out+"\n"), 0o700)
	path, err := WriteLaunch(dir, Launch{
		Dir:   dir,
		Env:   []string{"PATH=/usr/bin:/bin", "SHELL=" + shell, "SECRET=s3"},
		Argv:  []string{"sh", "-c", "echo claude $SECRET >> " + out},
		Shell: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("launch file mode: %v %v", fi.Mode(), err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunLaunchShell$")
	cmd.Env = append(os.Environ(), "GANTLINE_TEST_LAUNCH="+path)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, b)
	}
	// A #! script sees its own path as $0, so the login-style "-fakeshell"
	// argv[0] doesn't show here.
	got, _ := os.ReadFile(out)
	if want := "claude s3\nshell " + shell + " s3\n"; string(got) != want {
		t.Errorf("ran %q, want %q", got, want)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the launch file outlived the launch")
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "s3") {
		t.Error("the environment reached a command line")
	}
}
