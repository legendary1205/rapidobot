#!/usr/bin/env bash
#
# The installer is shipped by curl, not in the image, so nothing else would
# ever catch a break in it. These guard the failure modes that silently
# broke the sibling Rapido-Go installer in production:
#   - a helper that leaks a non-zero status kills `set -euo pipefail` with no
#     message at all;
#   - a prompt with no terminal attached loops forever instead of failing.
set -uo pipefail
cd "$(dirname "$0")/.."

fail=0
pass() { echo "  ok   $*"; }
bad()  { echo "  FAIL $*"; fail=1; }

bash -n rapidobot.sh && pass "rapidobot.sh parses" || bad "rapidobot.sh has a syntax error"

src=$(mktemp)
sed '/^main "\$@"$/d' rapidobot.sh > "$src"

# ask() must take the value from the environment without prompting.
out=$(BOT_TOKEN=abc bash -c 'set -euo pipefail; . "$1"; ask BOT_TOKEN "token"; echo "got=$BOT_TOKEN"' _ "$src" </dev/null 2>&1) || true
[ "$out" = "got=abc" ] && pass "ask() uses an environment value unattended" || bad "ask() with env set: $out"

# With nothing set and no terminal, ask() must fail fast - not hang.
out=$(timeout 10 bash -c 'set -euo pipefail; . "$1"; unset BOT_TOKEN; ask BOT_TOKEN "token"; echo SHOULD-NOT-REACH' _ "$src" </dev/null 2>&1)
rc=$?
if [ $rc -eq 124 ]; then
    bad "ask() hangs with no terminal"
elif echo "$out" | grep -q "No terminal"; then
    pass "ask() fails with a clear message when there is no terminal"
else
    bad "ask() without a terminal: rc=$rc out=$out"
fi

# The help screen prints real colours, never a literal backslash-033.
if bash rapidobot.sh help 2>&1 | grep -q '\033'; then
    bad "help prints raw escape codes"
else
    pass "help has no raw escape codes"
fi

rm -f "$src"
exit $fail
