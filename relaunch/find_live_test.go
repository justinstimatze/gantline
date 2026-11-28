package relaunch

import (
	"os"
	"strconv"
	"testing"
)

// TestFindLive reads a real pane's claude process when GANTLINE_LIVE_PID
// names the pane's process. It only reads process information.
func TestFindLive(t *testing.T) {
	pid, _ := strconv.Atoi(os.Getenv("GANTLINE_LIVE_PID"))
	if pid == 0 {
		t.Skip("GANTLINE_LIVE_PID not set")
	}
	s, err := Find(pid)
	if err != nil {
		t.Fatal(err)
	}
	argv, err := Argv(s.Argv, s.ID, "sonnet")
	t.Logf("pid %d dir %s sid %s env %d\nargv %q\nnew  %q (%v)", s.PID, s.Dir, s.ID, len(s.Env), s.Argv[:min(len(s.Argv), 4)], argv, err)
	if err != nil {
		t.Fatal(err)
	}
}
