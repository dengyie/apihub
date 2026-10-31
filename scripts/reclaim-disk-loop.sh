#!/bin/bash
#
# reclaim-disk-loop.sh -- supervisord-served loop that keeps /data from filling
# up with old backups.
#
# Why a loop and not cron: this host already runs its other maintenance jobs as
# supervisor programs (new-api-backup-loop, antigravity-log-prune), so restart
# policy and log rotation already exist here. Same shape they use: flock
# re-entrancy guard, quiet success, warn only on real problems, heartbeat every
# few rounds.
#
# The work itself is reclaim-disk.sh in this same directory. This wrapper only
# schedules it.
#
# Environment (all optional):
#   INTERVAL   seconds between runs (default 86400 = once a day)
#   HEARTBEAT  log a heartbeat every N rounds (default 1)

set -uo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
WORKER="$HERE/reclaim-disk.sh"
LOGF="${RECLAIM_LOG:-/data/new-api/logs/reclaim-disk.log}"
INTERVAL="${INTERVAL:-86400}"
HEARTBEAT="${HEARTBEAT:-1}"
LOCKF=/var/run/reclaim-disk.lock

mkdir -p "$(dirname "$LOGF")" 2>/dev/null || true
log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >> "$LOGF"; }
warn() { log "WARN: $*"; }

if [ ! -x "$WORKER" ]; then
  log "worker $WORKER missing or not executable; exiting"
  exit 1
fi

log "=== reclaim-disk-loop start pid=$$ interval=${INTERVAL}s ==="
round=0
while true; do
  round=$((round + 1))
  # -n: if a previous run still holds the lock, skip this round rather than
  # queue behind it. The fd wraps only the worker, never the sleep, so a slow
  # compress pass cannot hold the lock for the whole interval.
  if ! flock -n 9; then
    warn "previous run still holds the lock; skipping round $round"
  else
    if "$WORKER" >>"$LOGF" 2>&1; then
      (( round % HEARTBEAT == 0 )) && log "round $round ok"
    else
      warn "worker exited rc=$? (will retry next round)"
    fi
  fi 9>"$LOCKF"
  sleep "$INTERVAL"
done