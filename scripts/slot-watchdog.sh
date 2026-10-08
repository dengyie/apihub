#!/usr/bin/env bash
#
# slot-watchdog.sh -- safety net for any script that stops the APIHub slots.
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
# back. So arm this BEFORE stopping anything, from a separate process:
#
#     setsid nohup scripts/slot-watchdog.sh 240 /tmp/watchdog.log &
#
# It polls, and once nothing has been RUNNING for two consecutive checks it
# starts a slot and says so. When the API answers 200 again it retires.
#
# Usage: slot-watchdog.sh [grace-seconds] [logfile]
#   grace  how long to keep watching after arming (default 240)
#          Must exceed the longest expected outage, or it retires mid-repair.
set -uo pipefail

GRACE="${1:-240}"
LOG="${2:-/tmp/watchdog.log}"
CHECK_INTERVAL=2
STRIKES_REQ=2

log() { printf '%s slot-watchdog: %s\n' "$(date '+%H:%M:%S')" "$*" >>"$LOG"; }

# supervisorctl's exit code is 3 when anything is stopped, so it carries no
# information about this slot. Read the state word out of the output instead --
# the same mistake that caused the outage was reading an exit code as status.
slot_state() { supervisorctl status "$1" 2>/dev/null | awk '{print $2}'; }

any_running() {
  [[ $(slot_state apihub-blue) == RUNNING || $(slot_state apihub-green) == RUNNING ]]
}

# --noproxy: tebi's ~/.curlrc sets proxy=127.0.0.1:7897, so a plain curl
# measures the proxy rather than the app.
api_ok() {
  [[ $(curl -s -o /dev/null -w '%{http_code}' --noproxy '*' --max-time 3 \
       http://127.0.0.1:3000/api/status 2>/dev/null || echo 000) == 200 ]]
}

log "armed; grace=${GRACE}s"
deadline=$(( $(date +%s) + GRACE ))
strikes=0
started=0

while [[ $(date +%s) -lt $deadline ]]; do
  sleep "$CHECK_INTERVAL"
  if any_running; then strikes=0; continue; fi
  strikes=$((strikes + 1))
  log "nothing RUNNING (strike $strikes/$STRIKES_REQ)"
  if (( strikes >= STRIKES_REQ && started == 0 )); then
    log "starting apihub-green"
    if supervisorctl start apihub-green >>"$LOG" 2>&1; then
      log "start issued"
    else
      log "start FAILED -- check that the slot configs are valid"
    fi
    started=1
  fi
done

# Last chance before retiring, so it does not exit while the service is down.
for _ in $(seq 1 30); do
  api_ok && { log "api answering 200; retiring"; exit 0; }
  if (( started == 0 )); then
    log "api still down; starting apihub-green"
    supervisorctl start apihub-green >>"$LOG" 2>&1
    started=1
  fi
  sleep 2
done

log "RETIRING WITH THE SERVICE DOWN -- manual intervention needed"
exit 1