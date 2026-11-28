package queue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gantline/journal"
)

var fastVerify = VerifyOptions{Start: 300 * time.Millisecond, Finish: 300 * time.Millisecond, Command: 300 * time.Millisecond, Poll: 10 * time.Millisecond}

// transcriptLine is a synthetic transcript entry in one of the two shapes
// Claude Code 2.1.332 uses for a local command's output.
func transcriptLine(t time.Time, system bool, stdout string) string {
	content := "<local-command-stdout>" + stdout + "</local-command-stdout>"
	var e map[string]any
	if system {
		e = map[string]any{"type": "system", "subtype": "local_command", "timestamp": t.UTC().Format(time.RFC3339Nano), "content": content}
	} else {
		e = map[string]any{"type": "user", "timestamp": t.UTC().Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": content}}
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(!system) // both escaped and literal "<" must be read
	enc.Encode(e)
	return b.String()
}

// boundary marks a TestVerify stdout entry that stands for a
// compact_boundary transcript entry with the given trigger.
const boundary = "compact_boundary:"

// boundaryLine is a compact_boundary entry as Claude Code 2.1.332 writes it.
func boundaryLine(t time.Time, trigger string) string {
	b, _ := json.Marshal(map[string]any{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted",
		"timestamp": t.UTC().Format(time.RFC3339Nano), "compactMetadata": map[string]any{"trigger": trigger}})
	return string(b) + "\n"
}

func TestVerify(t *testing.T) {
	cases := []struct {
		name, cmd string
		events    []journal.Event
		stdout    []string
		want      string
	}{
		{"compact done", "/compact keep: x", []journal.Event{{Ev: "PreCompact"}, {Ev: "SessionStart", Source: "compact"}, {Ev: "PostCompact"}}, []string{boundary + "manual"}, Done},
		// A subagent's compaction fires the hooks but leaves the main transcript alone.
		{"compact hooks only", "/compact", []journal.Event{{Ev: "PreCompact"}, {Ev: "PostCompact"}}, nil, Unconfirmed},
		{"compact auto boundary", "/compact", []journal.Event{{Ev: "PreCompact"}, {Ev: "PostCompact"}}, []string{boundary + "auto"}, Unconfirmed},
		{"compact refused", "/compact", nil, []string{"Not enough messages to compact."}, Rejected},
		{"compact silent", "/compact", nil, nil, Unconfirmed},
		{"compact failed", "/compact", []journal.Event{{Ev: "PreCompact"}, {Ev: "StopFailure", Error: "rate_limit"}}, nil, Failed},
		{"model done", "/model sonnet", []journal.Event{{Ev: "PostModelSwitch", RequestedModel: "sonnet", ToModel: "claude-sonnet-5-5"}}, nil, Done},
		{"model other request", "/model sonnet", []journal.Event{{Ev: "PostModelSwitch", RequestedModel: "opus"}}, nil, Unconfirmed},
		{"model unknown", "/model nosuchmodel", nil, []string{"Model 'nosuchmodel' not found"}, Rejected},
		{"clear", "/clear", []journal.Event{{Ev: "SessionStart", Source: "clear"}}, nil, Done},
		{"mcp done", "/mcp reconnect echomcp", nil, []string{"Reconnected to echomcp"}, Done},
		{"mcp unknown", "/mcp reconnect nosuch", nil, []string{`MCP server "nosuch" not found`}, Rejected},
		{"slash output", "/context", nil, []string{"Context Usage 8%"}, Done},
		{"slash silent", "/context", nil, nil, Unconfirmed},
		{"prompt", "continue", []journal.Event{{Ev: "UserPromptSubmit"}}, nil, Done},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_STATE_HOME", dir)
			tr := filepath.Join(dir, "t.jsonl")
			typed := time.Now()
			// Output from before the command was typed must not count.
			body := transcriptLine(typed.Add(-time.Minute), true, "Reconnected to echomcp")
			// A manual boundary from before the command must not count either.
			body += boundaryLine(typed.Add(-time.Minute), "manual")
			for i, out := range c.stdout {
				if trig, ok := strings.CutPrefix(out, boundary); ok {
					body += boundaryLine(typed.Add(time.Millisecond), trig)
					continue
				}
				body += transcriptLine(typed.Add(time.Millisecond), i%2 == 0, out)
			}
			os.WriteFile(tr, []byte(body), 0o600)
			journal.Append("k", journal.Event{T: typed.Add(-time.Second).UnixNano(), Ev: "Stop", Transcript: tr})
			for i, e := range c.events {
				e.T = typed.Add(time.Duration(i+1) * time.Millisecond).UnixNano()
				journal.Append("k", e)
			}
			if got := Verify("k", c.cmd, typed, fastVerify); got.State != c.want {
				t.Fatalf("Verify(%q) = %+v, want %s", c.cmd, got, c.want)
			}
		})
	}
}

func TestLocalOutputsShapes(t *testing.T) {
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	at := time.Now()
	body := transcriptLine(at.Add(-time.Second), true, "old") + `{"type":"user","timestamp":"bad"}` + "\n" +
		transcriptLine(at.Add(time.Second), true, "\x1b[1mfrom system\x1b[0m") + transcriptLine(at.Add(2*time.Second), false, "from user")
	os.WriteFile(tr, []byte(body), 0o600)
	got := LocalOutputs(tr, at)
	if len(got) != 2 || got[0] != "from system" || got[1] != "from user" {
		t.Fatalf("LocalOutputs = %q", got)
	}
}

func TestCompactArgs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	before := time.Now().Add(-time.Minute)
	entry := func(ts time.Time, content string) string {
		b, _ := json.Marshal(map[string]any{"type": "user", "timestamp": ts.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": content}})
		return string(b) + "\n"
	}
	cmd := "<command-name>/compact</command-name>\n            <command-message>compact</command-message>\n            <command-args>keep: a &amp; b</command-args>"
	old := "<command-name>/compact</command-name>\n<command-message>compact</command-message>\n<command-args>older</command-args>"
	os.WriteFile(path, []byte(entry(before.Add(-time.Hour), old)+entry(time.Now(), cmd)), 0o600)
	got, ok := CompactArgs(path, before)
	if !ok || got != "keep: a & b" {
		t.Fatalf("CompactArgs = %q, %v", got, ok)
	}
	if _, ok := CompactArgs(path, time.Now().Add(time.Minute)); ok {
		t.Fatal("found a /compact older than t")
	}
}
