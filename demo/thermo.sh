#!/usr/bin/env bash
# A burn-rate thermometer for demo/record.sh burn. Claude Code runs it as:
#   thermo.sh statusline   the statusline: logs cost and limits, feeds
#                          `gantline statusline`, prints the last turn's burn
#   thermo.sh start        UserPromptSubmit hook: notes where the turn began
#   thermo.sh stop         Stop hook: the burn picks a /color, queued
#                          with `gantline q` when it changes
#   thermo.sh precompact   PreCompact hook: notes where the compaction began
#   thermo.sh compacted    PostCompact hook: the
#                          compaction is a model call too, so the statusline
#                          shows what it cost. No /color: one queued now would
#                          run only after the turn that follows the compaction.
#
# It meters what constrains you. On a subscription inside your limits, the
# color is how full the last hour's pace would leave the 5-hour window at
# its reset, or, with only a 7-day window, how many hours of work that pace
# leaves in it (holding a pace for the rest of the week would assume days of
# nonstop work). Either window at 100% counts as extra usage, and THERMO_METER=usd
# forces the dollar meter. Past a limit (extra
# usage) or with no limits reported (an API key), it is list-price spend per
# minute, Claude Code's own estimate (cost.total_cost_usd).
# A turn shorter than 30s counts as 30s, so a one-line reply can't read hot
# from a tiny denominator. State lives in .thermo/; while .thermo/off exists,
# stop does nothing.
set -u
st=.thermo
mkdir -p "$st"
now() { date +%s.%N; }
last() { tail -n 1 "$st/log" 2>/dev/null; }

case ${1:-} in
statusline)
  in=$(cat)
  # log: time cost 5h% 5h-reset 7d% 7d-reset ("-" when absent)
  printf '%s %s\n' "$(now)" "$(jq -r '[.cost.total_cost_usd // 0,
      .rate_limits.five_hour.used_percentage // "-", .rate_limits.five_hour.resets_at // "-",
      .rate_limits.seven_day.used_percentage // "-", .rate_limits.seven_day.resets_at // "-"]
    | map(tostring) | join(" ")' <<<"$in")" >> "$st/log"
  printf '%s' "$in" | gantline statusline >/dev/null 2>&1
  cat "$st/reading" 2>/dev/null || echo "burn: waiting for a turn to end"
  ;;
start)
  cat >/dev/null
  # A turn right after a compaction starts once the compaction's own cost has
  # settled, or that cost lands on the turn.
  for _ in $(seq 1 60); do [ -e "$st/compacting" ] || break; sleep 0.2; done
  printf '%s %s\n' "$(now)" "$(last | cut -d' ' -f2-)" > "$st/start"
  ;;
precompact)
  cat >/dev/null
  touch "$st/compacting"
  printf '%s %s\n' "$(now)" "$(last | cut -d' ' -f2-)" > "$st/start"
  ;;
stop|compacted)
  cat >/dev/null
  t1=$(now)
  [ "$1" = compacted ] && trap 'rm -f "$st/compacting"' EXIT
  [ -e "$st/off" ] && exit 0
  # The statusline logs the turn's final numbers a moment after Stop: wait
  # for an entry newer than Stop, then for the cost to hold for 1.5s.
  fresh= seen= since=
  for _ in $(seq 1 20); do
    sleep 0.5
    read -r l c _ <<<"$(last)"
    awk -v l="$l" -v t="$t1" 'BEGIN { exit !(l > t) }' || continue
    [ "$c" = "$seen" ] || { seen=$c since=$(now); continue; }
    awk -v a="$since" -v n="$(now)" 'BEGIN { exit !(n - a >= 1.5) }' && fresh=1 && break
  done
  # No numbers since Stop: this turn's spend isn't known yet, so leave the
  # color alone rather than read it as zero.
  [ -n "$fresh" ] || exit 0
  if [ "$1" = compacted ]; then
    read -r _ c0 _ < "$st/start"
    awk -v c0="$c0" -v c1="$(last | cut -d' ' -f2)" \
      'BEGIN { printf "compaction cost %.1f¢ at list price; it frees context, and the next turn rewrites the cache\n", (c1 - c0) * 100 }' > "$st/reading"
    exit 0
  fi
  # A /compact still queued runs before any /color queued now, so this turn's
  # color would show only after the compaction it no longer describes.
  gantline status --json 2>/dev/null |
    jq -e '.queue // [] | any(test("^q[0-9]+ /compact") and test(" (pending|running)$"))' >/dev/null && exit 0
  [ -s "$st/start" ] || exit 0
  # awk prints: color, then the reading for the statusline. Limits move in
  # whole percentage points, far coarser than one turn, so the limit meter
  # takes its pace from the last hour of the log (at least 15 minutes).
  hour=$(awk -v t="$t1" '$1 >= t - 3600 { print; exit }' "$st/log")
  out=$(awk -v t1="$t1" -v s="$(cat "$st/start")" -v h="$hour" -v e="$(last)" -v meter="${THERMO_METER:-}" '
    function band(x, a, b, c, d) { return x < a ? "cyan" : x < b ? "green" : x < c ? "yellow" : x < d ? "orange" : "red" }
    function left(sec) { return sec >= 3600 ? sprintf("%dh%02dm", sec / 3600, sec % 3600 / 60) : sprintf("%dm", sec / 60) }
    BEGIN {
      split(s, a, " "); split(h, o, " "); split(e, b, " ")
      for (i = 3; i <= 5; i += 2)               # 5h at fields 3-4, 7d at 5-6
        if (b[i] != "-" && b[i] + 0 >= 100) over = 1
      w = b[3] != "-" && o[3] != "-" ? 3 : b[5] != "-" && o[5] != "-" ? 5 : 0
      if (meter != "usd" && w && !over) {
        span = t1 - o[1]; if (span < 900) span = 900
        rate = (b[w] - o[w]) / span * 3600       # percentage points per hour
        if (w == 3) {
          # A 5-hour window: the color is how full this pace leaves it at reset.
          p = b[3] + rate * (b[4] - t1) / 3600
          print band(p, 50, 80, 100, 150)
          printf "burn %.1f%%/h of the 5h limit (now %d%%) · at this pace %.0f%% when it resets in %s\n", rate, b[3], p, left(b[4] - t1)
        } else {
          # A 7-day window: the color is how many hours of work this pace leaves.
          hrs = rate > 0 ? (100 - b[5]) / rate : 1e9
          print band(-hrs, -40, -20, -10, -5)
          if (rate > 0) printf "burn %.1f%%/h of the 7d limit (now %d%%) · %.0fh of work at this pace\n", rate, b[5], hrs
          else printf "burn 0%%/h of the 7d limit (now %d%%) over the last hour\n", b[5]
        }
      } else {
        dur = t1 - a[1]; if (dur < 30) dur = 30
        r = (b[2] - a[2]) * 100 / (dur / 60)
        print band(r, 1, 1, 3, 8)                 # cyan <1, yellow <3, orange <8, red
        printf "burn %.1f ¢/min at list price%s · session ~$%.3f\n", r, over ? " (past a limit: extra usage)" : "", b[2]
      }
    }')
  color=${out%%$'\n'*}
  printf '%s\n' "${out#*$'\n'}" > "$st/reading"
  if [ "$color" != "$(cat "$st/color" 2>/dev/null)" ]; then
    echo "$color" > "$st/color"
    gantline q "/color $color" >/dev/null 2>&1
  fi
  ;;
esac
exit 0
