package queue

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/justinstimatze/gantline/journal"
)

// Outcome is what a typed command turned out to do.
type Outcome struct {
	State  string
	Detail string
}

// VerifyOptions bounds the checks. Zero fields take the defaults.
type VerifyOptions struct {
	Start   time.Duration // a command must show it started within this; default 10s
	Finish  time.Duration // a started /compact must finish within this; default 15m
	Command time.Duration // a local command's output must appear within this; default 30s
	Poll    time.Duration // default 250ms
}

func (o VerifyOptions) withDefaults() VerifyOptions {
	if o.Start <= 0 {
		o.Start = 10 * time.Second
	}
	if o.Finish <= 0 {
		o.Finish = 15 * time.Minute
	}
	if o.Command <= 0 {
		o.Command = 30 * time.Second
	}
	if o.Poll <= 0 {
		o.Poll = 250 * time.Millisecond
	}
	return o
}

// Verify checks what cmd did after it was typed at typed, from key's
// journal and the session transcript. Facts from the P0 probes (Claude Code
// 2.1.332): /compact fires PreCompact then PostCompact; /model X fires
// PostModelSwitch with requested_model X; /clear fires SessionStart with
// source clear; /mcp fires no hook, so its result is read from the
// transcript's <local-command-stdout>.
func Verify(key, cmd string, typed time.Time, opt VerifyOptions) Outcome {
	opt = opt.withDefaults()
	wait := func(d time.Duration, match func(journal.Event) bool) (journal.Event, bool) {
		e, err := journal.WaitFor(key, typed, journal.WaitOptions{Timeout: d, Poll: opt.Poll}, match)
		return e, err == nil
	}
	outputs := func() []string { return LocalOutputs(transcript(key), typed) }
	rejection := func() (Outcome, bool) {
		for _, o := range outputs() {
			if strings.Contains(o, "not found") || strings.HasPrefix(o, "Failed") || strings.HasPrefix(o, "Error") {
				return Outcome{Rejected, o}, true
			}
		}
		return Outcome{}, false
	}
	f := strings.Fields(cmd)
	switch Kind(cmd) {
	case "compact":
		// An automatic compaction is not the one this command asked for.
		if _, ok := wait(opt.Start, func(e journal.Event) bool {
			return (e.Ev == "PreCompact" && e.Trigger != "auto") || e.Ev == "StopFailure"
		}); !ok {
			if o, ok := rejection(); ok {
				return o
			}
			if out := outputs(); len(out) > 0 {
				return Outcome{Rejected, out[len(out)-1]}
			}
			return Outcome{Unconfirmed, "no PreCompact within " + opt.Start.String()}
		}
		e, ok := wait(opt.Finish, func(e journal.Event) bool {
			return (e.Ev == "PostCompact" && e.Trigger != "auto") || e.Ev == "StopFailure"
		})
		switch {
		case !ok:
			return Outcome{Unconfirmed, "PreCompact but no PostCompact within " + opt.Finish.String()}
		case e.Ev == "StopFailure":
			return Outcome{Failed, "compaction failed: " + e.Error}
		}
		// A subagent's compaction fires the same hooks with the main
		// session's id but writes its boundary to its own transcript
		// (tclaude's claude_compaction_proof.go), so the main transcript
		// has the last word. Claude Code writes it just after the hooks.
		for deadline := time.Now().Add(opt.Finish); ; time.Sleep(opt.Poll) {
			if CompactedAfter(transcript(key), typed) {
				return Outcome{Done, "compacted"}
			}
			if time.Now().After(deadline) {
				return Outcome{Unconfirmed, "PostCompact, but no manual compaction in the main transcript within " + opt.Finish.String()}
			}
		}
	case "model":
		want := f[1]
		if e, ok := wait(opt.Start, func(e journal.Event) bool { return e.Ev == "PostModelSwitch" && e.RequestedModel == want }); ok {
			return Outcome{Done, "now " + e.ToModel + "; also saved as the default model for new sessions"}
		}
		if o, ok := rejection(); ok {
			return o
		}
		return Outcome{Unconfirmed, "no PostModelSwitch for " + want}
	case "clear":
		if _, ok := wait(opt.Start, func(e journal.Event) bool { return e.Ev == "SessionStart" && e.Source == "clear" }); ok {
			return Outcome{Done, "cleared"}
		}
		return Outcome{Unconfirmed, "no SessionStart(clear)"}
	case "mcp-reconnect":
		server := f[2]
		for deadline := time.Now().Add(opt.Command); ; time.Sleep(opt.Poll) {
			for _, o := range outputs() {
				if strings.HasPrefix(o, "Reconnected to "+server) {
					return Outcome{Done, o}
				}
			}
			if o, ok := rejection(); ok {
				return o
			}
			if time.Now().After(deadline) {
				return Outcome{Unconfirmed, "no reconnect result in the transcript"}
			}
		}
	case "slash":
		for deadline := time.Now().Add(opt.Command); ; time.Sleep(opt.Poll) {
			if out := outputs(); len(out) > 0 {
				return Outcome{Done, out[len(out)-1]}
			}
			if time.Now().After(deadline) {
				return Outcome{Unconfirmed, "no command output in the transcript"}
			}
		}
	case "prompt":
		if _, ok := wait(opt.Start, func(e journal.Event) bool { return e.Ev == "UserPromptSubmit" }); ok {
			return Outcome{Done, "turn started"}
		}
		return Outcome{Unconfirmed, "no UserPromptSubmit (are the hooks installed?)"}
	}
	return Outcome{Unconfirmed, ""}
}

// transcript is the newest transcript path the journal has seen for key.
func transcript(key string) string {
	st, _, _ := journal.Load(key)
	return st.Transcript
}

// CompactedAfter reports whether the transcript at path records a manual
// compaction (a compact_boundary entry) stamped after t. Only the last
// 4 MB of the file is read.
func CompactedAfter(path string, t time.Time) bool {
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > tailBytes {
		f.Seek(-tailBytes, io.SeekEnd)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256<<10), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), "compact_boundary") {
			continue
		}
		var e struct {
			Subtype   string    `json:"subtype"`
			Timestamp time.Time `json:"timestamp"`
			Meta      struct {
				Trigger string `json:"trigger"`
			} `json:"compactMetadata"`
		}
		if json.Unmarshal(line, &e) == nil && e.Subtype == "compact_boundary" && e.Meta.Trigger != "auto" && e.Timestamp.After(t) {
			return true
		}
	}
	return false
}

var stdoutTag = regexp.MustCompile(`(?s)<local-command-stdout>(.*?)</local-command-stdout>`)

// tailBytes bounds how much of a transcript LocalOutputs reads.
const tailBytes = 4 << 20

// LocalOutputs returns the text of every <local-command-stdout> entry in
// the transcript at path stamped after t, oldest first. Claude Code writes
// a local command's result as either a "system" entry with subtype
// local_command or a "user" entry, with the tag in a string content field.
// Only the last 4 MB of the file is read.
func LocalOutputs(path string, t time.Time) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > tailBytes {
		f.Seek(-tailBytes, io.SeekEnd)
	}
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256<<10), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		// The tag name alone: a JSON writer may escape "<" as \u003c.
		if !strings.Contains(string(line), "local-command-stdout") {
			continue
		}
		var e struct {
			Timestamp time.Time       `json:"timestamp"`
			Content   json.RawMessage `json:"content"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil || !e.Timestamp.After(t) {
			continue
		}
		for _, raw := range []json.RawMessage{e.Content, e.Message.Content} {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				continue
			}
			for _, m := range stdoutTag.FindAllStringSubmatch(s, -1) {
				out = append(out, strings.TrimSpace(stripANSI(m[1])))
			}
		}
	}
	return out
}

// QueuedSince reports whether Claude Code queued input after t: a
// "queue-operation" enqueue in the transcript at path, other than a
// background task's notification. Keys that never showed in the box may
// sit in that queue and run later, so they must not be typed again.
func QueuedSince(path string, t time.Time) bool {
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > tailBytes {
		f.Seek(-tailBytes, io.SeekEnd)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256<<10), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), "queue-operation") {
			continue
		}
		var e struct {
			Type      string    `json:"type"`
			Operation string    `json:"operation"`
			Timestamp time.Time `json:"timestamp"`
			Content   string    `json:"content"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "queue-operation" || e.Operation != "enqueue" || !e.Timestamp.After(t) {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(e.Content), "<task-notification>") {
			return true
		}
	}
	return false
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func stripANSI(s string) string { return ansi.ReplaceAllString(s, "") }
