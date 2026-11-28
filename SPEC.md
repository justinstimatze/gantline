# gantline design rules

These are the rules gantline's code keeps, each with the tests that fail when it breaks. Tests marked *corpus* need real screen captures, which aren't in this repository, and skip without them. Tests marked *live* need `-tags realclaude` and a Claude Code login. Run one with `go test ./... -run <name>`; `git grep -n 'func <name>'` finds its file.

## Rules

- **No daemon.** Claude Code hooks append to a per-pane journal and commands read it. A per-pane lock serializes every write into a pane, and one queue runner per pane is elected by lock. `TestSubmitHoldsPaneLock`, `TestLockWritesExcludes`, `TestRunSingleRunner`.
- **The journal holds no conversation content.** It stores event names, times, session IDs, the transcript path and a few short fields. It never stores prompt text, replies, tool input or compaction summaries. `TestFromHookKeepsNoContent`.
- **Nothing types over a person.** A write refuses when the box holds a draft, a dialog is open or the pane is in copy mode. The queue runner also waits out a running turn. It never clears a draft it didn't type. `TestSubmitRefusesDraft`, `TestSubmitRefusesDialog`, `TestRefusesInCopyMode`, `TestRunNeverClearsDraft`, `TestSubmitOwnNeverSubmitsSomeoneElsesDraft`; *live*: the `draft`, `permission` and `esc` scenarios.
- **Nothing answers a dialog.** Permission prompts and the "Switch model?" confirmation wait for a person, and the item records what it is waiting for. `TestModelSwitchDialog`; *live*: the `permission` scenario.
- **Only checked slash commands are queued.** `gantline q` takes a slash command only in a form a real session has run through it, since an unchecked one may open a dialog nothing closes. `--unchecked` lifts that, never for pickers, `/model`, `/config key=value` or commands that end or detach the session. `TestGuardAndKind`.
- **Keys are never typed twice.** Keys that may sit in Claude Code's own input queue, and an item a dead runner left running, are marked `unconfirmed` and not retyped. `TestRunConfirmsCompactFromClaudesQueue`, `TestRunSettlesOrphanedItem`; *live*: the `killed-runner` scenario.
- **Slash commands are typed, never pasted.** Claude Code sends a pasted slash command to the model as prompt text. `TestSubmitTypesLongSlashCommand`.
- **Outcomes are confirmed from hooks and the transcript.** The screen shows only what was typed. An item is `done` only on the event or transcript line its command produces, and `--then` follows only `done`. `TestFoldAndFooter`; *live*: `TestSelfControl`.
- **Outcomes reach the agent once each**, as at most 5 lines from the `UserPromptSubmit` hook. `TestFoldAndFooter`.
- **Status is brief.** `gantline status` prints a few lines by default, with `--json` and `--raw` on request. `TestStatusBriefIsBrief`.
- **Screen fingerprints are verified.** Every screen state gantline acts on is matched against real captures, and an unverified fingerprint never drives a keypress or a refusal. *corpus*: `TestFingerprintsVerified`, `TestCorpusLayout`.
- **Real captures stay out of git.** Fixtures in the repository are synthetic. `TestNoRealCapturesInGit`.
- **Install is reversible.** `gantline install` adds hooks and wraps the statusline without reordering or dropping anything else, and `uninstall` restores the statusline, even with no install record. `TestInstallUninstall`, `TestUnwrapStatusLine`.
- **A relaunch keeps secrets off command lines.** The session's environment goes to the new process through a 0600 file, and logs show only flag names. `TestRedact`, `TestRunLaunchShell`.

## Out of scope

- Answering permission prompts automatically. That would be a separate layer built on this one.
- Driving pickers by keyboard. Every command gantline needs has an argument form (`/mcp reconnect <server>`, `gantline model <name>`).
- An MCP server or a socket API. The CLI and the Go packages (`journal`, `queue`, `say`, `screen`) are the interfaces.

## Layout

| Package | Role |
|---|---|
| `cmd/gantline` | The CLI: `status`, `screen`, `q`, `model`, `restart`, `say`, `panes`, `audit`, `install`, and the internal `hook`, `statusline` and `launch` |
| `journal` | Hook events and statusline snapshots per pane, and the fold into an activity |
| `screen` | Cutting a capture into regions (box, mode line, dialog, last output) and the fingerprint table |
| `say` | Typing into a pane: chunked keys, bracketed paste, echo checks, Enter |
| `queue` | The per-pane queue, its runner and outcome checks |
| `internal/proc` | Another process's argv, environment, working directory and children, through gopsutil |
| `relaunch` | Reading a session's command line and rewriting it to resume on another model |
| `tmux` | The tmux calls |
| `internal/audit` | The delivery log and `gantline audit` |
| `cmd/chunkprobe` | A developer probe, not installed with gantline, that measures how large a `send-keys` argument Claude Code takes intact. Rerun it on a new Claude Code version |
