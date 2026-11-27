package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const userSettings = `{
  "model": "opus",
  "statusLine": {"type": "command", "command": "~/bin/my-statusline --flag 'x'", "padding": 0},
  "hooks": {
    "Stop": [
      {"matcher": "", "hooks": [{"type": "command", "command": "/home/u/go/bin/gantline hook stop", "timeout": 5}]},
      {"matcher": "", "hooks": [{"type": "command", "command": "other-stop-hook"}]}
    ],
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "weir"}]}]
  },
  "zeta": 1
}`

func TestInstallUninstall(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const self = "/opt/gl/gantline"
	out, err := installSettings([]byte(userSettings), self)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		StatusLine struct{ Command string } `json:"statusLine"`
		Hooks      map[string][]struct {
			Hooks []struct {
				Command string
				Async   bool
			}
		}
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, ev := range hookEvents {
		n := 0
		for _, g := range got.Hooks[ev] {
			for _, h := range g.Hooks {
				if strings.Contains(h.Command, "gantline") {
					n++
					if h.Command != self+" hook" || h.Async == syncHook(ev) {
						t.Errorf("%s: entry %+v", ev, h)
					}
				}
			}
		}
		if n != 1 {
			t.Errorf("%s: %d gantline entries, want 1 (the old `hook stop` replaced)", ev, n)
		}
	}
	if !strings.Contains(string(out), "other-stop-hook") || !strings.Contains(string(out), `"weir"`) {
		t.Error("install dropped someone else's hook")
	}
	want := self + ` statusline -- sh -c '~/bin/my-statusline --flag '\''x'\'''`
	if got.StatusLine.Command != want {
		t.Errorf("statusLine = %s\nwant      %s", got.StatusLine.Command, want)
	}
	if strings.Index(string(out), `"model"`) > strings.Index(string(out), `"zeta"`) {
		t.Error("install reordered the file's keys")
	}
	again, err := installSettings(out, self)
	if err != nil || string(again) != string(out) {
		t.Errorf("install is not idempotent:\n%s", again)
	}

	// Uninstall restores the statusLine install recorded.
	writeRecordForTest(t, "/s/settings.json", `{"type": "command", "command": "~/bin/my-statusline --flag 'x'", "padding": 0}`)
	back, err := uninstallSettings(out, "/s/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(back), "gantline") {
		t.Errorf("uninstall left gantline in:\n%s", back)
	}
	if !strings.Contains(string(back), `"command": "~/bin/my-statusline --flag 'x'"`) || !strings.Contains(string(back), "other-stop-hook") {
		t.Errorf("uninstall lost the statusline or another hook:\n%s", back)
	}

	// With no record for this file (state deleted, or another --settings),
	// uninstall reads the wrapped command back out of the statusline.
	for _, rec := range []string{"", "/other/settings.json"} {
		os.Remove(recordPath())
		if rec != "" {
			writeRecordForTest(t, rec, `{"type": "command", "command": "wrong"}`)
		}
		back, err := uninstallSettings(out, "/s/settings.json")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(back), `"command": "~/bin/my-statusline --flag 'x'"`) || !strings.Contains(string(back), `"padding": 0`) {
			t.Errorf("record %q: uninstall didn't unwrap the statusline:\n%s", rec, back)
		}
	}
}

func TestUnwrapStatusLine(t *testing.T) {
	for _, inner := range []string{"plain", "~/bin/x --flag 'y'", "a'b'c", ""} {
		if got, ok := unwrapStatusLine("/g/gantline statusline -- sh -c " + shellQuote(inner)); inner != "" && (!ok || got != inner) {
			t.Errorf("unwrap(%q) = %q, %v", inner, got, ok)
		}
	}
	if _, ok := unwrapStatusLine("/g/gantline statusline"); ok {
		t.Error("gantline's own statusline has nothing to unwrap")
	}
}

func writeRecordForTest(t *testing.T, settings, statusLine string) {
	rec, _ := json.Marshal(installRecord{Settings: settings, StatusLine: json.RawMessage(statusLine)})
	if err := os.WriteFile(recordPath(), rec, 0o600); err != nil {
		t.Fatal(err)
	}
}
