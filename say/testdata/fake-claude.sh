#!/usr/bin/env bash
# A stand-in for Claude Code's input box, for tests. Draws the box the way
# 2.1.332 does ("❯" plus U+00A0 between two rules) and echoes typed keys.
#   FAKE_SWALLOW=N  ignore the first N Enter presses (paste-swallowed Enter)
#   FAKE_HEADSUP=1  show a "Heads up" notice that eats keys until "0"
#   FAKE_DRAFT=txt  start with txt already in the box
#   FAKE_LOG=txt    show txt as conversation output above the box
#   FAKE_COLLAPSE=N collapse the first N inputs that reach 20 chars into
#                   "[Pasted text #k]", as Claude Code does with a burst
#   FAKE_BUSY=1     mode line shows a turn running (with a background shell)
#   FAKE_DIALOG=1   a permission dialog replaces the box; any key answers it
#   FAKE_MENU=1     Enter on a box starting "/mo" completes it to "/model "
#                   instead of submitting, like the slash autocomplete menu
#   FAKE_GHOST=TEXT  draw TEXT dimmed in the empty box, like a prompt suggestion
#   FAKE_CONFIRM=1  a submitted "/model X" opens a "Switch model?" dialog
#                   under a "▔" rule; 1 or Enter confirms (and emits the
#                   switch), 2 goes back
#   FAKE_BUSY_ON_INPUT=1 a turn starts as soon as the box holds text, as a
#                   background task's notification can start one mid-typing
#   FAKE_DEAF_SECS=N drop every key for the first N seconds, as a session
#                   still starting up does
#   FAKE_LATE_SECS=N hold every key typed in the first N seconds and draw
#                   them in the box afterwards, unsubmitted, as a busy
#                   session's input can arrive once its turn ends
#   FAKE_STATUS=path write a statusline snapshot naming the session and model
#   Arguments: --model M and --resume ID, as claude takes them; with
#   --resume and FAKE_EVENTS it reports SessionStart source=resume for ID.
#   FAKE_BUSY_SECS=N busy for the first N seconds, redrawing as time passes
#   FAKE_EVENTS=path on each submit, append the journal events Claude Code's
#                   hooks would record (the P0 orders): /compact → PreCompact,
#                   SessionStart(compact), PostCompact; /model X →
#                   PostModelSwitch(requested X); /clear → SessionStart(clear);
#                   any other text → UserPromptSubmit then Stop
#   FAKE_TRANSCRIPT=path on /compact, also append the compact_boundary
#                   entry Claude Code writes to the main transcript
#   FAKE_BOX_ROWS=N wrap the box at 36 characters and show at most N rows,
#                   as a short pane does: past N the top rule reads
#                   "─── ↑ K more ───" and the ❯ row scrolls away. C-u then
#                   deletes one wrapped row per press, as Claude Code's does.
# It turns on bracketed paste, as Claude Code does, and shows a paste as
# "[Pasted text #k]" while keeping the full text for SUBMITTED.
set -u
stty raw -echo
printf '\033[?2004h'
busy=${FAKE_BUSY:-0}
dialog=${FAKE_DIALOG:-0}
menu=${FAKE_MENU:-0}
confirm=${FAKE_CONFIRM:-0}
confirming=0
pending=""
events=${FAKE_EVENTS:-}
busy_until=0
deaf_until=$(( $(date +%s) + ${FAKE_DEAF_SECS:-0} ))
late_until=$(( $(date +%s) + ${FAKE_LATE_SECS:-0} ))
late=""
[ -n "${FAKE_BUSY_SECS:-}" ] && busy_until=$(( $(date +%s) + FAKE_BUSY_SECS ))
isbusy() { [ "$busy" = 1 ] || { [ "$busy_until" != 0 ] && [ "$(date +%s)" -lt "$busy_until" ]; }; }
emit() { [ -n "$events" ] && printf '{"t":%s,"ev":"%s"%s}\n' "$(date +%s%N)" "$1" "${2:-}" >> "$events"; }
submitted() {
	case "$1" in
	/compact*) emit PreCompact ',"trigger":"manual"'; emit SessionStart ',"source":"compact"'; emit PostCompact ',"trigger":"manual"'
		[ -n "${FAKE_TRANSCRIPT:-}" ] && printf '{"type":"system","subtype":"compact_boundary","timestamp":"%s","compactMetadata":{"trigger":"manual"}}\n' "$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)" >> "$FAKE_TRANSCRIPT" ;;
	"/model "*) emit PostModelSwitch ",\"requested_model\":\"${1#/model }\",\"to_model\":\"claude-${1#/model }\"" ;;
	/clear*) emit SessionStart ',"source":"clear"' ;;
	/*) : ;;
	*) emit UserPromptSubmit; emit Stop ;;
	esac
}
fmodel=haiku fsid=""
while [ $# -gt 0 ]; do
	case "$1" in
	--model) fmodel=$2; shift ;;
	--resume) fsid=$2; shift ;;
	esac
	shift
done
fmodel=${FAKE_FORCE_MODEL:-$fmodel} # FAKE_FORCE_MODEL=M: start on M whatever --model says
[ -n "$fsid" ] && emit SessionStart ",\"source\":\"resume\",\"sid\":\"$fsid\""
[ -n "${FAKE_STATUS:-}" ] && printf '{"t":%s,"sid":"%s","model_id":"claude-%s-5-5"}\n' "$(date +%s%N)" "$fsid" "$fmodel" > "$FAKE_STATUS"
inpaste=0
pbuf=""
full=""
swallow=${FAKE_SWALLOW:-0}
headsup=${FAKE_HEADSUP:-0}
buf=${FAKE_DRAFT:-}
collapse=${FAKE_COLLAPSE:-0}
pasted=0
log=${FAKE_LOG:-}
rule="────────────────────────────────────────"
rows=${FAKE_BOX_ROWS:-0}
wrap=36
# boxlines sets top (the rule above the box) and box (its rows) for $buf.
boxlines() {
	top=$rule box=()
	if [ "$rows" -le 0 ] || [ ${#buf} -le $wrap ]; then
		box=("❯ $buf")
		return
	fi
	local i n start=0
	local parts=()
	for ((i = 0; i < ${#buf}; i += wrap)); do parts+=("${buf:i:wrap}"); done
	n=${#parts[@]}
	if [ "$n" -gt "$rows" ]; then
		start=$((n - rows))
		top="─── ↑ $start more ──────────────────────────────"
	fi
	for ((i = start; i < n; i++)); do
		if [ "$i" -eq 0 ]; then box+=("❯ ${parts[i]}"); else box+=("  ${parts[i]}"); fi
	done
}
draw() {
	printf '\033[2J\033[H'
	printf '  fake claude\r\n'
	[ -n "$log" ] && printf '%s\r\n' "$log"
	if [ "$headsup" = 1 ]; then
		printf '  Heads up: settings changed\r\n  0: Dismiss\r\n'
	fi
	boxlines
	if [ "$confirming" = 1 ] || [ "$dialog" = 1 ] || [ -z "$buf" ]; then top=$rule; fi
	printf '%s\r\n' "$top"
	if [ "$confirming" = 1 ]; then
		printf '%s\r\n' "▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔"
		printf '  Switch model?\r\n  Your next response will be slower\r\n  ❯ 1. Yes, switch\r\n    2. No, go back\r\n  Enter to confirm · Esc to cancel\r\n'
		return
	fi
	if [ "$dialog" = 1 ]; then
		printf ' Bash command\r\n touch f\r\n Do you want to proceed?\r\n ❯ 1. Yes\r\n   2. No\r\n Esc to cancel · Tab to amend\r\n'
		return
	fi
	if [ -z "$buf" ] && [ -n "${FAKE_GHOST:-}" ]; then
		printf '\033[39m❯ \033[2m%s\033[0m\r\n' "$FAKE_GHOST"
	elif [ -z "$buf" ]; then
		printf '❯ Try "refactor <filepath>"\r\n'
	else
		printf '%s\r\n' "${box[@]}"
	fi
	printf '%s\r\n' "$rule"
	if isbusy; then
		printf '  ⏵⏵ auto mode on · 1 shell · esc to interrupt · ↓ to manage\r\n'
	else
		printf '  ⏵⏵ auto mode on (shift+tab to cycle)\r\n'
	fi
}
draw
drawn_busy=$(isbusy && echo 1 || echo 0)
while true; do
	IFS= read -rsn1 -t 0.2 c
	rc=$?
	if [ $rc -gt 128 ] && [ -n "$late" ] && [ "$(date +%s)" -ge "$late_until" ]; then
		buf+="$late"
		late=""
		draw
		continue
	fi
	if [ $rc -gt 128 ]; then # timeout: redraw only if the busy line changed
		now=$(isbusy && echo 1 || echo 0)
		[ "$now" != "$drawn_busy" ] && { drawn_busy=$now; draw; }
		continue
	fi
	[ $rc -ne 0 ] && break
	[ -n "${FAKE_DEAF_SECS:-}" ] && [ "$(date +%s)" -lt "$deaf_until" ] && continue
	if [ -n "${FAKE_LATE_SECS:-}" ] && [ "$(date +%s)" -lt "$late_until" ]; then
		late+="$c"
		continue
	fi
	if [ "$c" = $'\033' ]; then
		IFS= read -rsn5 seq
		case "$seq" in
		"[200~") inpaste=1; pbuf="" ;;
		"[201~")
			inpaste=0
			pasted=$((pasted + 1))
			full="$buf$pbuf"
			buf="[Pasted text #$pasted]"
			[ "${FAKE_BUSY_ON_INPUT:-0}" = 1 ] && busy=1
			draw ;;
		esac
		continue
	fi
	if [ "$inpaste" = 1 ]; then
		pbuf+="$c"
		continue
	fi
	if [ "$confirming" = 1 ]; then
		confirming=0
		if [ "$c" = 1 ] || [ "$c" = $'\r' ] || [ -z "$c" ]; then
			log="CONFIRMED: $pending"
			submitted "$pending"
		else
			log="WENT BACK: $pending"
		fi
	elif [ "$dialog" = 1 ]; then
		log="DIALOG ANSWERED: $c"
		dialog=0
	elif [ "$headsup" = 1 ]; then
		[ "$c" = 0 ] && headsup=0
	elif [ "$c" = $'\r' ] || [ -z "$c" ]; then
		if [ "$swallow" -gt 0 ]; then
			swallow=$((swallow - 1))
		elif [ "$menu" = 1 ] && [ "${buf#/mo}" != "$buf" ] && [ "$buf" != "/model " ]; then
			buf="/model "
		elif [ -n "$full" ]; then
			log="PASTED+SUBMITTED: $full"
			submitted "$full"
			buf=""
			full=""
		elif [ "$confirm" = 1 ] && [ "${buf#/model }" != "$buf" ]; then
			pending="$buf"
			buf=""
			confirming=1
		elif [ -n "$buf" ]; then
			log="SUBMITTED: $buf"
			submitted "$buf"
			buf=""
		fi
	elif [ "$c" = $'\x15' ] && [ "$rows" -gt 0 ] && [ -z "$full" ]; then # C-u: one wrapped row
		buf=${buf:0:$(( (${#buf} - 1) / wrap * wrap ))}
	elif [ "$c" = $'\x15' ]; then # C-u
		buf=""
		full=""
	elif [ "${buf#\[Pasted}" != "$buf" ]; then
		: # a collapsed paste ignores further typing until cleared
	else
		buf+="$c"
		if [ "$collapse" -gt 0 ] && [ ${#buf} -ge 20 ]; then
			collapse=$((collapse - 1))
			pasted=$((pasted + 1))
			buf="[Pasted text #$pasted]"
		fi
	fi
	[ "${FAKE_BUSY_ON_INPUT:-0}" = 1 ] && [ -n "$buf" ] && busy=1
	draw
done
