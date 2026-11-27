#!/bin/sh
# Block the first Stop only, forcing the turn to continue with a slow tool call.
m="$(dirname "$0")/.blocked"
[ -e "$m" ] && exit 0
touch "$m"
echo '{"decision":"block","reason":"Before stopping, run the bash command: sleep 8   and then reply with the single word continued."}'
