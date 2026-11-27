- To run a built-in slash command on yourself (`/compact`, `/context`, `/clear`, `/mcp reconnect <server>`),
  make `gantline q [--then '<message>'] '<command>'` your last tool call, then end your turn. The command
  is typed after your turn ends. Don't wait or poll for it. Its outcome arrives with your next prompt as a
  line like `gantline: q3 /compact … done`. `--then` types its message only when the outcome is `done`.
- On `unconfirmed`, check `gantline status` and `gantline screen --region last` before you retry: the
  command may have run. On `expired` (the pane was never idle in time) or `rejected`, fix the cause before
  you queue it again. On `failed`, tell the user.
- To restart yourself, run `gantline restart`; to switch your own model, run `gantline model <name>`.
  Make it your last tool call too. The restart ends your background tasks, watches and `/loop`, and a
  follow-up message asks you to re-arm them, so note what was running before you restart.
- `gantline q` takes `/compact`, `/clear`, `/context`, `/mcp reconnect <server>` and `/color <color>`. It refuses
  other slash commands, since many open a dialog nothing would close; `--unchecked` queues one anyway. Never
  queue `/model` (it changes every new session's default); use `gantline model <name>`.
  Don't use `--pane` or `gantline say` on another pane unless the user asks.
- To read your own state, run `gantline status`. To read your screen, run `gantline screen`
  (`--region last`, `--find '<regex>'`). To read what a queued command printed, run
  `gantline q --show <id>`, or add `--screen` for what the pane showed when it finished.
