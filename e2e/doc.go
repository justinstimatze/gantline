// Package e2e drives a real Claude Code session through gantline: the
// session reads its own state and screen, compacts itself and carries on,
// switches model and reconnects an MCP server, all by queueing its own
// commands. Every check reads the journal, the queue and files the session
// wrote, never the screen.
//
// It costs real model calls, so it runs only on demand:
//
//	go test -tags realclaude ./e2e -count=1 -v -timeout 30m
//
// GANTLINE_E2E_RUNS=N repeats it N times; GANTLINE_E2E_BLOCKSTOP=1 adds a
// Stop hook that blocks the first stop, so the queue has to wait out a turn
// that continues after a Stop.
package e2e
