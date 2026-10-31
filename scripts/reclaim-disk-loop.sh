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
#   INTERVAL       seconds between runs (default 86400 = once a day)
#   HEARTBEAT      log a heartbeat every N rounds (default 1)
#   MAX_LOG_BYTES  rotate the log once it grows past this (default 1 MiB)
#
# The log rotates itself. This loop is the only writer, it runs for the life of
# the host, and nothing else on this box covers /data/new-api/logs -- the
# archive-logs job prunes the application's own logs, not this file. Left alone
# it grows forever, which is the same "maintenance job fills the disk it is
# supposed to protect" failure the worker exists to prevent.

set -uo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
WORKER="$HERE/reclaim-disk.sh"
LOGF="${RECLAIM_LOG:-/data/new-api/logs/reclaim-disk.log}"
INTERVAL="${INTERVAL:-86400}"
HEARTBEAT="${HEARTBEAT:-1}"
MAX_LOG_BYTES="${MAX_LOG_BYTES:-1048576}"
LOCKF=/var/run/reclaim-disk.lock

mkdir -p "$(dirname "$LOGF")" 2>/dev/null || true

# Keep one previous generation, compressed: two files, bounded, and the history
# of the last rotation is still readable without a log daemon.
rotate_log() {
  local size
  # -f before wc: the input redirect is resolved before any 2>/dev/null takes
  # effect, so a missing log would otherwise print an error every round.
  [ -f "$LOGF" ] || return 0
  # `wc -c` pads its output on some platforms (BSD), so trim before comparing;
  # an untrimmed size fails the numeric test below and rotation would silently
  # never happen.
  size=$(wc -c <"$LOGF" 2>/dev/null | tr -d '[:space:]')
  [[ $size =~ ^[0-9]+$ ]] || return 0
  (( size > MAX_LOG_BYTES )) || return 0
  # Compress to a scratch name first: if gzip fails halfway, the previous
  # archive is still in place instead of a truncated one.
  rm -f "$LOGF.rotating"
  if gzip -c "$LOGF" >"$LOGF.rotating" 2>/dev/null && [ -s "$LOGF.rotating" ]; then
    mv -f "$LOGF.rotating" "$LOGF.1.gz"
    rm -f "$LOGF.1"
    : >"$LOGF"
  else
    # No usable gzip. Keep the log as a plain archive rather than truncating it:
    # rotation is housekeeping and must never be the thing that loses data.
    rm -f "$LOGF.rotating"
    mv -f "$LOGF" "$LOGF.1" 2>/dev/null || true
    rm -f "$LOGF.1.gz"
  fi
}

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >> "$LOGF"; }
warn() { log "WARN: $*"; }

# Sourcing with RECLAIM_LOOP_LIB=1 exposes the helpers above for testing without
# starting the loop. Same shape deploy.sh uses.
if [[ ${RECLAIM_LOOP_LIB:-0} == 1 ]]; then
  return 0 2>/dev/null || exit 0
fi

if [ ! -x "$WORKER" ]; then
  log "worker $WORKER missing or not executable; exiting"
  exit 1
fi

log "=== reclaim-disk-loop start pid=$$ interval=${INTERVAL}s ==="
round=0
while true; do
  round=$((round + 1))
  # Rotate before the worker appends, so the file this round writes to is the
  # fresh one and the previous round's output lands in the archive intact.
  rotate_log
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