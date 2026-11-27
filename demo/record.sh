#!/usr/bin/env bash
# Records a gantline demo: one real, unedited Claude Code session in a
# throwaway tmux server, with `gantline status` live in a pane below it and
# a seconds clock in the status bar.
#
#   demo/record.sh tool      # writes demo/tool.cast and demo/tool.gif
#   demo/record.sh burn
#   demo/record.sh color
#   demo/record.sh compact
#
# Run it in a sandbox, as gantline itself: the tool demo has the model write
# an MCP server and restart into it, which runs whatever it registered.
#
# Needs claude, tmux, asciinema 2.x, agg, jq and gantline on PATH. GANTLINE_DEMO_MODEL picks the model
# (default haiku). The .cast keeps real time; only the GIF caps pauses, at
# GANTLINE_DEMO_IDLE seconds (default 2), so the clock shows every cut.
set -euo pipefail

name=${1:-tool}
model=${GANTLINE_DEMO_MODEL:-haiku}
idle=${GANTLINE_DEMO_IDLE:-2}
out=$(cd "$(dirname "$0")" && pwd)
cols=150 rows=44

for c in claude tmux asciinema agg gantline jq; do
  command -v "$c" >/dev/null || { echo "record.sh: $c not found" >&2; exit 1; }
done

S=gldemo$$
T="tmux -L $S"
D=$(mktemp -d "${XDG_CACHE_HOME:-$HOME/.cache}/gldemo-XXXX")
rec=
cleanup() {
  [ -n "$rec" ] && kill "$rec" 2>/dev/null || true
  [ -n "${GANTLINE_DEMO_KEEP:-}" ] && $T capture-pane -p -t %0 > "$GANTLINE_DEMO_KEEP/${name:-demo}-pane.txt" 2>/dev/null
  $T kill-server 2>/dev/null || true
  rm -rf "$D"
}
trap cleanup EXIT
export GANTLINE_TMUX_SOCKET=$S

# The project. The session loads only these settings and this .mcp.json,
# none of yours, so the recording shows no hooks or servers of your own:
# gantline's hooks and statusline, the shell, file edits and the demo's own
# MCP tool. Anything else is refused without a prompt (dontAsk), so the session
# sees the refusal and adapts instead of waiting on a dialog nobody answers.
mkdir -p "$D/.claude"
jq -n --arg home "$HOME" '
  ["Notification","PermissionRequest","PostCompact","PostModelSwitch","PostToolUse","PreCompact",
   "SessionEnd","SessionStart","Stop","StopFailure","UserPromptSubmit"]
  | map({key: ., value: [{matcher: "", hooks: [{type: "command", command: "gantline hook", timeout: 5}
      + (if . == "UserPromptSubmit" or . == "SessionStart" then {} else {async: true} end)]}]})
  | from_entries
  | {hooks: ., statusLine: {type: "command", command: "gantline statusline"},
     # None of your instructions either: your global CLAUDE.md and rules, any
     # CLAUDE.md above the project, and auto-memory.
     claudeMdExcludes: [$home + "/.claude/**", $home + "/CLAUDE.md", $home + "/.cache/CLAUDE.md"],
     autoMemoryEnabled: false,
     enableAllProjectMcpServers: true, promptSuggestionEnabled: false, spinnerTipsEnabled: false,
     permissions: {defaultMode: "dontAsk",
       allow: ["Bash", "mcp__dice", "Read", "Write", "Edit"]}}' > "$D/.claude/settings.local.json"
echo '{"mcpServers": {}}' > "$D/.mcp.json"
steps=() warm= legend= after_compact= title=
claude="claude --model $model --setting-sources project,local --strict-mcp-config --mcp-config .mcp.json"

code=$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')
case $name in
tool)
  # The session gets the note a gantline user keeps in CLAUDE.md, and a task
  # it has no tool for. The prompt names the two commands; the server, the
# restart message and the call are the session's own.
  cat > "$D/CLAUDE.md" <<'MD'
To restart yourself in place, which reloads settings, hooks and MCP servers, run `gantline restart --then '<message>'` as the last step of a turn: gantline relaunches this session on the same conversation once the turn ends, then types the message. Register an MCP server for this project with `claude mcp add --scope project <name> -- <command>`.
MD
  prompt="Roll a random number from 1 to 1,000,000 with an MCP tool you build, then write it to done.txt. Build it as dice.py, a stdio MCP server in standard-library Python 3. Register it with claude mcp add, then restart yourself with gantline so it loads. Nobody is watching this session: finish without asking me anything, and don't roll it any other way."
  limit=480
  ;;
color)
  # A lie detector: the prompt bar shows how the session knows each answer.
  printf 'def add(a, b):\n    return a - b\n' > "$D/calc.py"
  printf '%s\n' 'What does add(2, 3) in calc.py return?' \
    'Why did whoever wrote calc.py use a minus sign?' \
    'Will this repo get 1,000 GitHub stars?' > "$D/questions.txt"
  prompt="This is a gantline demo: your prompt bar is a lie detector. Answer the questions in questions.txt one at a time. Before each answer, set your prompt bar: green if you will check the answer in a file or with a command, yellow if you can only infer it, red if you would be guessing. To set it, run gantline q --then 'Answer question N in one sentence, then go on.' '/color COLOR' (N is the question number) and end your turn with no other text. After the last answer, run these in one Bash call, joined with &&: gantline q '/color red', then the same for orange, yellow, green, cyan, blue, purple and pink, then gantline q --then 'Write done.txt containing ok.' '/color default'. Then end your turn with no other text."
  limit=420
  ;;
burn)
  # A Stop hook turns each turn's burn into a /color. The demo meters
  # list-price dollars: a few cents of Haiku never moves a whole percentage
  # point of a usage limit.
  cp "$out/thermo.sh" "$D/thermo.sh"
  jq --arg t "THERMO_METER=usd ./thermo.sh" '
    .statusLine.command = ($t + " statusline") | .statusLine.refreshInterval = 1
    | .hooks.UserPromptSubmit[0].hooks += [{type: "command", command: ($t + " start")}]
    | .hooks.Stop[0].hooks += [{type: "command", command: ($t + " stop"), async: true}]
    | .hooks.PreCompact[0].hooks += [{type: "command", command: ($t + " precompact")}]
    | .hooks.PostCompact[0].hooks += [{type: "command", command: ($t + " compacted"), async: true}]' \
    "$D/.claude/settings.local.json" > "$D/s.json" && mv "$D/s.json" "$D/.claude/settings.local.json"
  for i in $(seq 1 600); do printf 'line %04d: %s\n' "$i" "the quick brown fox jumps over the lazy dog while the kettle hums and the clock ticks on"; done > "$D/big.txt"
  echo 'last line: the thermometer was right all along' >> "$D/big.txt"
  warm="Reply with just: ready"
  prompt="Say hi in three words."
  steps=("Read big.txt in full and tell me its last line." "What was its first line?"
    "You are done with big.txt. Compact yourself: run gantline q --then 'Say bye in two words.' '/compact Keep only: this is a gantline demo.' and end your turn with no other text.")
  after_compact=1
  title="the prompt bar shows spend per minute"
  legend=" ¢/min: #[fg=cyan]under 1 #[fg=yellow]under 3 #[fg=colour208]under 8 #[fg=red]8 and up#[default] "
  mkdir -p "$D/.thermo" && touch "$D/.thermo/off"
  limit=300
  ;;
compact)
  prompt="This is a gantline demo. The codeword is $code. Count to 8 in log.txt, one number per line, one Edit or Write call per number. After you write 4, compact yourself: run gantline q --then 'Carry on counting from where log.txt ends.' '/compact Keep: the codeword, and that the task is counting to 8 in log.txt, then writing done.txt.' and end your turn with no other text. After 8, run gantline status, then write done.txt containing only the codeword."
  limit=420
  ;;
*)
  echo "record.sh: unknown demo $name (have: tool, color, burn, compact)" >&2; exit 2 ;;
esac

# remain-on-exit keeps the pane if claude exits at launch, so the failure can
# be read instead of surfacing later as "can't find pane".
echo 'set -g remain-on-exit on' > "$D/tmux.conf"
$T -f "$D/tmux.conf" new-session -d -s demo -x $cols -y $((rows - 1)) -c "$D" "$claude"
sleep 2
if [ "$($T display -p -t %0 '#{pane_dead} #{pane_dead_status}')" != "0 " ]; then
  echo "record.sh: claude exited at launch ($($T display -p -t %0 'status #{pane_dead_status}')):" >&2
  $T capture-pane -p -t %0 | grep -v '^\s*$' >&2
  exit 1
fi
$T set -g status-interval 1
$T set -g focus-events on
$T set -g status-style 'bg=colour236,fg=colour250'
$T set -g status-left " gantline demo: $name${title:+ · $title} "
$T set -g status-left-length 80
$T set -g status-right ' %H:%M:%S '
[ -n "$legend" ] && $T set -g status-right "$legend %H:%M:%S " && $T set -g status-right-length 80
$T split-window -v -l 11 -t demo -c "$D" \
  "while :; do s=\$(gantline status --pane %0 2>&1); tput cup 0 0; printf '%s\n' \"\$s\" | sed 's/\$/\x1b[K/'; tput ed; sleep 1; done"
$T select-pane -t %0

# Answer the folder-trust dialog, then wait for an empty input box.
for _ in $(seq 1 60); do
  c=$($T capture-pane -p -t %0)
  if grep -q 'trust this folder' <<<"$c"; then
    $T send-keys -t %0 Down; sleep 0.3; $T send-keys -t %0 Enter
  elif [ "$(gantline status --pane %0 --json 2>/dev/null | jq -r '"\(.dialog // "")/\(.box)"')" = /empty ]; then
    break # gantline sees no dialog and an empty box: a ❯ alone also marks a dialog's choice
  fi
  sleep 1
done
sleep 2

# A turn has ended and nothing new started for at least $1 seconds. After a
# turn, status reads "stopped", and "idle" only once Claude Code's 60s idle
# notice arrives.
quiet() {
  local s
  s=$(gantline status --pane %0 2>/dev/null) || return 1
  [[ ${s%%$'\n'*} =~ (idle|stopped)\ for\ ([0-9]+)(s|m) ]] &&
    { [ "${BASH_REMATCH[3]}" = m ] || [ "${BASH_REMATCH[2]}" -ge "$1" ]; }
}

# The warm-up turn writes the prompt cache before recording starts, so the
# first recorded turn doesn't read hot for that alone.
if [ -n "$warm" ]; then
  gantline say %0 "$warm" >/dev/null
  sleep 3
  for _ in $(seq 1 60); do quiet 3 && break; sleep 1; done
  rm -f "$D/.thermo/off"
  $T send-keys -t %0 C-l
fi

asciinema rec -q --overwrite --cols $cols --rows $rows \
  -c "env -u TMUX tmux -L $S attach -t demo" "$out/$name.cast" </dev/null >/dev/null 2>&1 &
rec=$!
sleep 2
gantline say %0 "$prompt"
for step in "${steps[@]}"; do
  sleep 3
  for _ in $(seq 1 120); do quiet 6 && break; sleep 1; done
  gantline say %0 "$step" >/dev/null
done
if [ ${#steps[@]} -gt 0 ]; then
  sleep 3
  # A queued /compact and its follow-up are still to come: wait for both.
  if [ -n "$after_compact" ]; then
    for _ in $(seq 1 180); do
      [ "$(gantline status --pane %0 --json 2>/dev/null | jq -r '.compactions // 0')" -ge 1 ] && break; sleep 1
    done
    sleep 8
  fi
  for _ in $(seq 1 120); do quiet 6 && break; sleep 1; done
  sleep 2
  echo ok > "$D/done.txt"
fi

# Stop early on a prompt or dialog: nothing in the demo answers one.
stuck=0
for _ in $(seq 1 "$limit"); do
  [ -s "$D/done.txt" ] && break
  if gantline status --pane %0 2>/dev/null | head -n 1 | grep -q -e awaiting_permission -e '· dialog'; then
    stuck=$((stuck + 1))
  else
    stuck=0
  fi
  if [ $stuck -ge 30 ]; then
    echo "record.sh: stopped at a dialog nobody will answer" >&2; break
  fi
  sleep 1
done
sleep 4
$T detach-client -s demo 2>/dev/null || true
wait "$rec" 2>/dev/null || true
rec=

if [ -s "$D/done.txt" ]; then
  echo "done.txt: $(cat "$D/done.txt") (codeword was $code)"
  [ -f "$D/dice.py" ] && cp "$D/dice.py" "$out/$name-dice.py"
else
  echo "record.sh: no done.txt after ${limit}s; the recording shows how far it got" >&2
fi

# Scrub the home path, user name and plan name out of the recording. The
# banner draws the plan after a cursor move (\u001b[C), so match it with no
# word boundary in front.
email=$(git config user.email 2>/dev/null || true)
plans='Ultra|Enterprise|Max|Pro|Team'
# Every replacement keeps the original's width. tmux redraws by moving the
# cursor to absolute columns, so a shorter stand-in leaves stale characters
# wherever tmux later writes at the old column. Claude Code shortens long
# lines with "…", which can cut a home path short of the user name, so any
# /home/<name> prefix is replaced too, at its own length.
NAME=$USER PLANS=$plans EMAIL=$email perl -pi -e '
  BEGIN { $f = substr("demouser" . ("x" x 64), 0, length $ENV{NAME}) }
  s#/home/([A-Za-z0-9._-]+)#"/home/" . substr($f x 8, 0, length $1)#ge;
  s#\Q$ENV{NAME}\E#$f#g;
  s#(\\u001b\[C|Claude )($ENV{PLANS})\b#$1 . (" " x length $2)#ge;
  if (length $ENV{EMAIL}) { s#\Q$ENV{EMAIL}\E#substr("user\@example.com" . ("x" x 99), 0, length $ENV{EMAIL})#ge }
' "$out/$name.cast"
# Check the text as a viewer reads it too: escapes and line breaks removed,
# so a name the terminal wrapped across two lines still matches.
flat=$(jq -r '.[2]? // empty' "$out/$name.cast" | sed -E 's/\x1b\[[0-9;?]*[A-Za-z]//g; s/\x1b[()][A-Z0-9]//g' | tr -d '\r\n')
if grep -n -i -e "$USER" ${email:+-e "$email"} "$out/$name.cast" >/dev/null ||
  grep -q -i -e "$USER" -e "${USER:0:5}" -e "$HOME" ${email:+-e "$email"} <<<"$flat" ||
  grep -E -o '/home/[A-Za-z0-9._-]+' <<<"$flat" | grep -q -v -E '^/home/(demouser|demo|d|de|dem|demou|demous|demouse)x*$' ||
  grep -n -E "(\\\\u001b\\[C|Claude )($plans)\\b" "$out/$name.cast" >/dev/null; then
  echo "record.sh: $out/$name.cast still names you; not converting" >&2; exit 1
fi
# Cut the cast where tmux detaches (it resets modes, 7727 among them, and
# clears the screen) so the GIF ends on the session, not a blank terminal.
awk '/\[\?7727l/ { exit } { print }' "$out/$name.cast" > "$out/$name.cast.tmp" && mv "$out/$name.cast.tmp" "$out/$name.cast"
agg --idle-time-limit "$idle" --font-size 14 "$out/$name.cast" "$out/$name.gif" >/dev/null
[ -f "$D/.thermo/reading" ] && echo "last reading: $(cat "$D/.thermo/reading"), color $(cat "$D/.thermo/color" 2>/dev/null)"
# The log holds your usage percentages: it goes to GANTLINE_DEMO_KEEP, never demo/.
[ -n "${GANTLINE_DEMO_KEEP:-}" ] && [ -f "$D/.thermo/log" ] && cp "$D/.thermo/log" "$GANTLINE_DEMO_KEEP/$name-thermo.log"
echo "wrote $out/$name.cast and $out/$name.gif"
