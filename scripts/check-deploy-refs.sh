#!/usr/bin/env bash
#
# check-deploy-refs.sh -- refuse a second copy of the deployment layout.
#
# WHY THIS EXISTS
#
# Every path the deployment uses is derived from one variable, DEFAULT_APP_ROOT
# in scripts/deploy.sh. Two places had their own handwritten copy of it, and
# both failed silently rather than loudly:
#
#   * .github/workflows/deploy.yml read /tmp/mnt/new-api/.apihub-deploy-state
#     directly. When the root moved to /data the script followed and this copy
#     did not, so the step whose entire job is to tell an operator whether the
#     deploy worked printed "last backup: none" -- every time, with no error.
#   * .env.example documented APIHUB_STATIC_DIR under the retired root, so
#     anyone configuring from it pointed at a directory that no longer exists.
#
# A duplicate that is wrong is worse than no duplicate: it does not fail, it
# answers. That is why this is a check and not a comment.
#
# It also enforces the other half of the same lesson: a safeguard nothing starts
# is not a safeguard. scripts/slot-watchdog.sh was correct code that was armed
# once by hand and gone after the next reboot, while the runbook described it as
# standing protection. If it is no longer wired into deploy.sh, it should not
# exist -- and this is what says so.
#
# Comments are exempt. Explaining which path used to be wrong, and why, is the
# opposite of a second copy of it -- the failures above were invisible precisely
# because nobody wrote down what the old value was.

set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

FAIL=0
fail() { printf '  FAIL %s\n     %s\n' "$1" "$2"; FAIL=1; }
pass() { printf '  ok   %s\n' "$1"; }

# scan <pattern> [pathspec...] -- matching lines in tracked files, minus the
# ones that are only explaining the pattern in a comment.
scan() {
  local pat="$1"; shift
  git ls-files -z -- "$@" 2>/dev/null \
    | xargs -0 grep -nIE -e "$pat" 2>/dev/null \
    | grep -vE ':[0-9]+:[[:space:]]*#' || true
}

hits="$(scan '/tmp/mnt' . ':!scripts/deploy.sh')"
if [[ -z $hits ]]; then
  pass "no leftover references to the retired /tmp/mnt root"
else
  fail "no leftover references to the retired /tmp/mnt root" "$hits"
fi

# The rollback pointer belongs to deploy.sh alone. A workflow that reads it has
# its own copy of the layout, and that copy is the thing that goes stale.
hits="$(scan '\.apihub-deploy-state' .github)"
if [[ -z $hits ]]; then
  pass "no workflow reads the deploy state file behind deploy.sh's back"
else
  fail "no workflow reads the deploy state file behind deploy.sh's back" "$hits"
fi

# Same reasoning for the deployment root: a workflow that spells it out is a
# second copy. Asking deploy.sh is the only way to stay correct across a move.
hits="$(scan '/(tmp|opt|var|home)/[a-zA-Z0-9._-]+/new-api' .github)"
if [[ -z $hits ]]; then
  pass "no workflow hardcodes a deployment root"
else
  fail "no workflow hardcodes a deployment root" "$hits"
fi

if [[ -f scripts/slot-watchdog.sh ]]; then
  if grep -q 'slot-watchdog.sh' scripts/deploy.sh; then
    pass "the slot guard is installed and registered by deploy.sh"
  else
    fail "the slot guard is installed and registered by deploy.sh" \
         "scripts/slot-watchdog.sh exists but nothing references it; wire it in or delete it"
  fi
else
  pass "no unreferenced slot guard"
fi

if (( FAIL )); then
  echo
  echo "deployment layout check failed"
  exit 1
fi
echo
echo "deployment layout ok"