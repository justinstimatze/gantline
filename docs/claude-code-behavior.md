# Observed Claude Code behavior

gantline relies on how Claude Code 2.1.332 behaves, and much of that behavior isn't documented. These are the observations its code depends on. Each came from a throwaway session driven by a probe hook or a real e2e run. The raw captures are kept on the machine that took them and are not in this repository; the tests that read them skip without them.

`gantline status` warns on any Claude Code version other than 2.1.332. To check it, run the e2e tests and scenarios from the README on that version, and compare `claude --help` with the `flags` table in `relaunch/argv.go`. Together they exercise the facts below. If they pass, `mkdir -p ~/.local/state/gantline/corpus/<version>` marks the version checked on that machine.

## Hooks

- **A turn** is `UserPromptSubmit`, then any number of `PreToolUse` and `PostToolUse`, then `Stop`. A turn the session starts on its own, such as a background task finishing, also fires `UserPromptSubmit`.
- **A blocked stop** shows as a later `Stop` with `stop_hook_active: true` and tool events in between. A `Stop` alone never means the session is idle.
- **An Esc interrupt fires no hook.** `Stop` doesn't fire either. Neither does pressing Esc to deny a permission prompt. An Esc while the `UserPromptSubmit` hooks run puts the prompt back in the input box as a draft.
- **A permission prompt** fires `PermissionRequest` at once and `Notification` with `permission_prompt` about 6 seconds later.
- **`/compact`** fires `PreCompact` (`trigger: manual`, `custom_instructions` set to the argument), then `SessionStart` with `source: compact`, then `PostCompact` with `compact_summary`. `SessionStart` comes before `PostCompact`. An automatic compaction has `trigger: auto`. Just after those hooks, the main transcript gets a `system` entry with subtype `compact_boundary` and `compactMetadata.trigger`. A subagent's compaction fires the same hooks under the main session's id but writes its boundary to its own transcript under `subagents/` (tclaude's [`claude_compaction_proof.go`](https://github.com/tofutools/tclaude/blob/main/pkg/claude/harness/claude_compaction_proof.go), observed on 2.1.314; not reproduced here), so gantline confirms a `/compact` only once the main transcript has a manual boundary.
- **`/clear`** fires `SessionStart` with `source: clear` and a new session ID.
- **`/model`** fires `PreModelSwitch` and `PostModelSwitch` with `requested_model` and `to_model`. It also saves the model as the default for every new session, in `~/.claude/settings.json`.
- **`/mcp`** fires no hook. Its outcome, like other local commands', lands in the transcript as `<local-command-stdout>`: `Reconnected to echomcp`, or `MCP server "nosuch" not found`.
- **Hook output** reaches the model only from a synchronous hook. gantline registers `UserPromptSubmit` and `SessionStart` without `async` for that reason, and the rest as `async`.

## Screen

- **The busy marker** is an `esc to interrupt` segment in the mode line under the input box. It isn't always the last segment: with a background shell the line reads `… · esc to interrupt · ↓ to manage`.
- **A permission prompt** replaces the mode line, which hides the busy marker while it is up. It shows `Do you want to proceed?`, numbered options starting at `❯ 1. Yes`, and `Esc to cancel · Tab to amend`.
- **During a compaction** the input box shows empty while the mode line says `esc to interrupt`. A check for an empty box alone would type into it.
- **A partial slash command** (`/mo`) opens a menu with the first match highlighted, and Enter selects it. With arguments there is no menu. gantline presses Enter on a slash command once and never re-presses it.
- **The dimmed suggestion** Claude Code shows in an empty box looks like a draft in a plain capture. gantline captures with escape codes and drops dim text on the prompt line.
- **No scrollback.** Claude Code draws in the alternate screen, for which tmux keeps no history. In a short pane, the output of `/context` is gone once the next turn draws, so the queue runner saves the block above the box as soon as a slash command finishes.

## Typing

- **One `send-keys` argument of 850 bytes or more** collapses into a `[Pasted text #N]` placeholder; 800 bytes arrives intact. gantline types in 256-byte chunks with a 5 ms gap, and sends a trailing `;` as `\;`.
- **A bracketed paste** (`load-buffer`, then `paste-buffer -p`) delivers long text whole. The box shows `[Pasted text #1]`, and the transcript holds every byte.
- **A pasted slash command never runs.** Claude Code sends it to the model as prompt text instead. gantline always types slash commands, whatever their length; a typed 1,010-byte `/compact` ran in every e2e run.
- **Keys typed while a turn runs** can sit in Claude Code's own input queue and run later. gantline never types such keys again; it marks the item `unconfirmed`, except for `/compact`, which it keeps watching for until the item's deadline.

## Relaunching

- **`claude --resume <id> --model <name>`** in the same directory, with the same environment, brings back the same session ID on the new model and leaves `settings.json` alone. The old process sends `SessionEnd`, and the new one `SessionStart` with `source: resume`.
- **A restart ends background work**: background tasks, Monitor watches and `/loop` schedules don't survive it.
- **`claude --help` lists every flag** gantline's command-line rewrite accepts. It refuses unknown flags, print mode and worktree launches. The `flags` table in `relaunch/argv.go` lists them, each with how it takes a value.

## Prior art

- The [`compact-self` gist](https://gist.github.com/27medkamal/820124876cf750967c14b1ebe55c95f8) uses a Stop hook to mark the turn end, then types `/compact` into `$TMUX_PANE` in 40-character chunks, refusing to type into copy mode. gantline's send path started from it.
- [claude-yolo](https://github.com/claude-yolo/claude-yolo) (MIT) polls `capture-pane` and presses Enter with a cooldown, to answer permission prompts.
- [agent-yes](https://github.com/snomiao/agent-yes) (MIT) and [claude-auto](https://github.com/yukkes/claude-auto) (MIT) answer prompts from the screen and from the transcript.
- [ccmanager](https://github.com/kbwo/ccmanager) (MIT) recognizes session states from screen text with a stability debounce.
- [tclaude](https://github.com/tofutools/tclaude) (MIT) tracks session state from hooks, sends `/compact` with `send-keys`, and confirms the compaction from the main transcript's `compact_boundary` before its follow-up. gantline's transcript check follows it.
