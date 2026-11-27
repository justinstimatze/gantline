package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/justinstimatze/gantline/internal/audit"
	"github.com/justinstimatze/gantline/journal"
)

// auditResult is one keystroke delivery or relaunch, checked against its
// pane's journal at the moment it began.
type auditResult struct {
	At      time.Time
	Pane    string
	Key     string
	Kind    string // keys or relaunch
	ID      string
	CmdKind string
	Reasons []string // why the journal says the pane was busy; none is clean
	NoData  bool     // the journal no longer reaches back that far
}

// auditRecords checks every keys and relaunch line in say.jsonl since t.
func auditRecords(since time.Time) ([]auditResult, error) {
	f, err := os.Open(filepath.Join(audit.StateDir(), "say.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	events := map[string][]journal.Event{}
	var out []auditResult
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r struct {
			Kind    string `json:"kind"`
			Pane    string `json:"pane"`
			Key     string `json:"key"`
			ID      string `json:"id"`
			CmdKind string `json:"cmd_kind"`
			Start   int64  `json:"start_ns"`
		}
		if json.Unmarshal(sc.Bytes(), &r) != nil || (r.Kind != "keys" && r.Kind != "relaunch") || r.Key == "" {
			continue
		}
		at := time.Unix(0, r.Start)
		if at.Before(since) {
			continue
		}
		evs, ok := events[r.Key]
		if !ok {
			evs, _ = journal.Events(r.Key)
			events[r.Key] = evs
		}
		res := auditResult{At: at, Pane: r.Pane, Key: r.Key, Kind: r.Kind, ID: r.ID, CmdKind: r.CmdKind}
		if len(evs) == 0 || evs[0].Time().After(at) {
			res.NoData = true
		} else {
			res.Reasons = journal.BusyAt(evs, at)
		}
		out = append(out, res)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, sc.Err()
}

// auditCmd handles `gantline audit [--since D] [--e2e]`: every keystroke
// delivery and relaunch the queue runner made, checked against the pane's
// journal for a running turn, a tool call just after a Stop, or an open
// compaction at the moment it began.
func auditCmd(args []string) {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	since := fs.Duration("since", 24*time.Hour, "how far back to look")
	e2e := fs.Bool("e2e", false, "include the e2e test's throwaway panes")
	fs.Parse(args)
	res, err := auditRecords(time.Now().Add(-*since))
	if err != nil {
		fmt.Fprintln(os.Stderr, "gantline audit:", err)
		os.Exit(1)
	}
	clean, suspect, nodata := 0, 0, 0
	for _, r := range res {
		if !*e2e && strings.Contains(r.Key, "gl-e2e") {
			continue
		}
		switch {
		case r.NoData:
			nodata++
		case len(r.Reasons) == 0:
			clean++
		default:
			suspect++
			fmt.Printf("%s %s %s %s %s: %s\n", r.At.Format(time.DateTime), r.Pane, r.Kind, r.ID, r.CmdKind, strings.Join(r.Reasons, "; "))
		}
	}
	fmt.Printf("gantline audit: %d deliveries since %s: %d clean, %d busy by the journal, %d older than the journal\n",
		clean+suspect+nodata, time.Now().Add(-*since).Format(time.DateTime), clean, suspect, nodata)
	if suspect > 0 {
		os.Exit(1)
	}
}
