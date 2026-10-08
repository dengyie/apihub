#!/usr/bin/env bash
#
# slot-watchdog.sh -- last-resort guard: if neither APIHub slot is RUNNING, start one.
#
# WHY THIS EXISTS
#
# On 2026-10-09 the database migration stopped both slots and then died on its
# own next line. The line was:
#
#     supervisorctl status apihub-blue apihub-green | sed 's/^/  /'
#
# tebi's supervisorctl exits 3 whenever a program is stopped -- that is its
# documented "at least one program is not running" status, not an error. The
# script ran under `set -euo pipefail`, so that ordinary exit code aborted it
# immediately after the stop. Nothing restarted the service for 73 minutes.
#
# The lesson is not "check that line's exit code". It is that the process which
# takes a service down must never be the only thing responsible for bringing it
# back -- and, more sharply, that a safeguard nobody starts is not a safeguard.
# An earlier version of this file was armed by hand with `setsid nohup` and was
# therefore gone after the next reboot, while the runbook described it as a
# standing protection. It is now a supervisord program: deploy.sh installs it
# and registers it on every deploy, so there is no step to forget.
#
# WHAT IT DOES NOT DO
#
# It is not a deploy tool. It never writes a config, never stops anything, and
# never chooses which release to serve -- it only starts a slot that is not
# running, from whatever config is already on disk. Anything smarter than that
# belongs in deploy.sh, which is the thing that understands the handover.
#
# Usage: slot-watchdog.sh [poll-seconds] [trigger-seconds]
#
#   poll     seconds between checks (default 2)
#   trigger  seconds with NO slot running before acting (default 60)
#
# The trigger is a floor, not a reaction time. A handover never has zero slots
# running, so it never trips. `deploy.sh --rollback` does stop both on purpose,
# and its zero-running window is a config write plus one `supervisorctl start`
# -- far inside the default. A repair that overruns the trigger *should* be
# rescued, so there is deliberately no way for that path to disable the guard.

set -uo pipefail

POLL="${1:-2}"
TRIGGER="${2:-60}"

# Green first, blue second. Both bind the same port with SO_REUSEPORT, so
# either is a working service; the order only decides which one gets tried
# first when both are equally valid. Read the state WORD rather than
# supervisorctl's exit code -- the same mistake that caused the outage was
# reading an exit code as status, since tebi exits 3 for any stopped program.
slot_state() { supervisorctl status "$1" 2>/dev/null | awk '{print $2}'; }

any_running() {
  local s
  for s in apihub-blue apihub-green; do
    [[ $(slot_state "$s") == RUNNING ]] && return 0
  done
  return 1
}

log() { printf '%s slot-guard: %s\n' "$(date '+%F %T')" "$*"; }

bring_something_up() {
  local s
  for s in apihub-green apihub-blue; do
    log "nothing RUNNING for ${down}s; starting $s"
    if supervisorctl start "$s" 2>&1 | sed 's/^/  start: /'; then
      log "start issued for $s"
      return 0
    fi
    log "could not start $s"
  done
  return 1
}

log "guard up; poll=${POLL}s trigger=${TRIGGER}s"
down=0
while :; do
  sleep "$POLL"
  if any_running; then
    (( down > 0 )) && log "a slot is RUNNING again after ${down}s down"
    down=0
    continue
  fi
  down=$((down + POLL))
  (( down < TRIGGER )) && continue
  # Act at most once per continuous outage: after issuing a start, wait for a
  # real slot to appear rather than firing supervisorctl every poll interval.
  if bring_something_up; then
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      sleep "$POLL"
      any_running && break
    done
    if any_running; then
      log "service restored"
      down=0
    else
      log "a start was issued but nothing came up; retrying on the next trigger"
    fi
  fi
done