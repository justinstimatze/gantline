package main

import (
	"testing"
	"time"

	"github.com/justinstimatze/gantline/internal/audit"
	"github.com/justinstimatze/gantline/journal"
)

func TestAuditRecords(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Now()
	ev := func(ago time.Duration, name string) {
		journal.Append("k", journal.Event{T: now.Add(-ago).UnixNano(), Ev: name})
	}
	ev(60*time.Second, "UserPromptSubmit")
	ev(50*time.Second, "Stop")
	ev(20*time.Second, "UserPromptSubmit")
	keys := func(ago time.Duration, id string) {
		audit.Write(audit.Rec{Kind: "keys", Pane: "%1", Extra: map[string]any{"key": "k", "id": id, "start_ns": now.Add(-ago).UnixNano()}})
	}
	keys(40*time.Second, "q1")                      // after the Stop: clean
	keys(10*time.Second, "q2")                      // mid-turn: busy
	keys(2*time.Minute, "q0")                       // before the journal starts
	audit.Write(audit.Rec{Kind: "say", Pane: "%1"}) // not a queue delivery
	res, err := auditRecords(now.Add(-time.Hour))
	if err != nil || len(res) != 3 {
		t.Fatalf("auditRecords = %+v, %v", res, err)
	}
	if !res[0].NoData || len(res[1].Reasons) != 0 || len(res[2].Reasons) != 1 {
		t.Fatalf("verdicts: %+v", res)
	}
}
