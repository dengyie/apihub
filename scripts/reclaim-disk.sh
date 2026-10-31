#!/bin/bash
#
# reclaim-disk.sh -- keep the 98G root overlay from filling up with backups.
#
# The problem: every ops run against the live database cuts a full SQLite copy
# (new-api.db.bak_<what>_<timestamp>), and nothing ever removed them. /data hit
# 68% with 51 copies of a 600MB database, and the deploy/manual backup dirs added
# another ~12G. None of that is live data; all of it is rollback material.
#
# The policy is compress-in-place rather than delete:
#   * A backup exists so a rollback has something to fall back on. Deleting it
#     throws away the only artifact that can reconstruct a state nobody has
#     needed yet -- exactly the thing you want AFTER an incident.
#   * These are SQLite files, so they compress roughly 8:1. Keeping them all and
#     gzipping the older ones costs ~1/8 the space and loses nothing.
#   * Only the newest DB_KEEP copies stay uncompressed, so the rollback you are
#     most likely to want is still a plain `cp` away.
#
# What gets compressed (never deleted):
#   * new-api.db.bak_* / new-api.db.bak-* / new-api.db.preembed-* in the data
#     dir -- the bulk of the problem.
#   * the ignored binary/Db inside backups-snapshot/pre_deploy_* dirs (their
#     small config files are tracked by git and stay untouched).
#   * the full DB copies stashed by the channel-deletion playbook under
#     data/deleted-channels/. The rollback .sql/.csv rows next to them are tiny
#     and stay as they are.
#
# What is never touched:
#   * the live database and its -wal/-shm sidecars. A hot SQLite cannot be
#     compressed, and its sidecars are meaningless without the live file.
#   * the .apihub-deploy-state rollback target and deploy.sh's timestamped
#     backup dirs -- deploy.sh owns their retention (BACKUP_KEEP).
#
# Compression is verified before the original is removed (gzip -t on the
# fresh archive), so an interrupted run cannot lose a backup. Idempotent:
# already-compressed files are skipped. --dry-run lists what would happen.
#
# Usage: reclaim-disk.sh [--dry-run]
# Env:   DB_KEEP  newest DB copies to leave uncompressed (default 4)

set -uo pipefail

NA=/data/new-api
DATA_DIR="$NA/data"
SNAP_DIR="$NA/backups-snapshot"

DB_KEEP="${DB_KEEP:-4}"
DB_DAYS="${DB_DAYS:-3}"
DRY=0
for a in "$@"; do [[ $a == "--dry-run" ]] && DRY=1; done

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"; }
warn() { log "WARN: $*"; }

saved_kb=0

# compress one file: gzip -9 to a sibling .gz, verify, then drop the original.
# The temp name is unique per pid so two runs can never write the same target.
compress_one() { # $1 path  $2 label
  local f="$1" label="$2" sz gz before
  sz=$(du -k "$f" 2>/dev/null | cut -f1); sz=${sz:-0}
  if (( DRY )); then
    log "[dry-run] $label: gzip $f (${sz}KB)"
    saved_kb=$((saved_kb + sz * 7 / 8))
    return 0
  fi
  # Write the archive on the SAME filesystem as the source: a cross-mount temp
  # would need twice the space on the tight filesystem and could fail halfway.
  gz="$f.gz.tmp.$$"
  if ! gzip -9 -c "$f" > "$gz" 2>/dev/null; then
    warn "$label: gzip failed for $f; leaving original in place"
    rm -f "$gz"; return 1
  fi
  if ! gzip -t "$gz" 2>/dev/null; then
    warn "$label: gzip verification failed for $f; leaving original in place"
    rm -f "$gz"; return 1
  fi
  if ! mv -f "$gz" "$f.gz"; then
    warn "$label: cannot rename $gz; leaving original in place"
    rm -f "$gz"; return 1
  fi
  before=$(du -k "$f.gz" 2>/dev/null | cut -f1); before=${before:-0}
  rm -f "$f"
  saved_kb=$((saved_kb + sz - before))
  log "$label: $f ${sz}KB -> ${before}KB"
}

# --- 1. SQLite copies in the data dir --------------------------------------
# newest-first: DB_KEEP stay uncompressed, and anything younger than DB_DAYS is
# left alone too (an in-flight ops sequence keeps its snapshots readable).
kept=0
cand=$(ls -1t "$DATA_DIR"/new-api.db.bak_* "$DATA_DIR"/new-api.db.bak-* \
              "$DATA_DIR"/new-api.db.preembed-* 2>/dev/null || true)
while IFS= read -r f; do
  [[ -n $f && -f $f && ! -L $f ]] || continue
  case $f in *.gz) continue ;; esac
  if (( kept < DB_KEEP )); then kept=$((kept + 1)); continue; fi
  if [[ $(find "$f" -mtime -"$DB_DAYS" -print -quit 2>/dev/null) != "" ]]; then
    kept=$((kept + 1)); continue
  fi
  compress_one "$f" "db-bak"
done <<< "$cand"

# --- 2. pre_deploy_* dirs in backups-snapshot ------------------------------
# Only the git-ignored payload (historical binary + full DB copy). The tracked
# .conf files are a few KB and are in git anyway, so they stay as they are.
for d in "$SNAP_DIR"/pre_deploy_*; do
  [ -d "$d" ] || continue
  for f in "$d/new-api" "$d/new-api.db"; do
    [[ -f $f ]] || continue
    case $f in *.gz) continue ;; esac
    compress_one "$f" "predeploy"
  done
done

# --- 3. channel-deletion stashes ------------------------------------------
# A purge stashes a full DB copy here before deleting rows. The .sql/.csv
# rollback rows next to it are tiny; only the .db copies are worth compacting.
while IFS= read -r f; do
  [[ -n $f && -f $f ]] || continue
  case $f in *.gz|*-wal|*-shm|*-journal) continue ;; esac
  [[ $(find "$f" -mtime -"$DB_DAYS" -print -quit 2>/dev/null) != "" ]] && continue
  compress_one "$f" "deleted-channels-db"
done <<< "$(find "$DATA_DIR/deleted-channels" -type f -name '*.db*' 2>/dev/null || true)"

# --- 4. hand-named backup dirs --------------------------------------------
# The manual-*/pre-* dirs under backups/ hold a single full DB copy each, cut
# before some hand operation. deploy.sh's timestamped dirs and the active
# rollback target are deploy.sh's to manage and stay untouched; only DB files
# inside the hand-named dirs are compacted, and restoring one is a one-liner
# (gunzip) if a rollback to it ever becomes necessary.
state_base=""
if [ -f "$NA/.apihub-deploy-state" ]; then
  state_base=$(basename "$(cat "$NA/.apihub-deploy-state" 2>/dev/null)" 2>/dev/null)
fi
while IFS= read -r f; do
  [[ -n $f && -f $f ]] || continue
  case $f in *.gz|*-wal|*-shm|*-journal) continue ;; esac
  dir=$(dirname "$f")
  base=$(basename "$dir")
  [[ $base =~ ^[0-9]{8}-[0-9]{6}$ ]] && continue
  [[ -n $state_base && $base == "$state_base" ]] && continue
  [[ $(find "$f" -mtime -"$DB_DAYS" -print -quit 2>/dev/null) != "" ]] && continue
  compress_one "$f" "hand-backup-db"
done <<< "$(find "$NA/backups" -type f \( -name 'new-api.db' -o -name 'new-api.db.bak*' \) 2>/dev/null || true)"

log "run complete: reclaimed ~$((saved_kb / 1024))MB"
df -h "$NA" | tail -1 | awk '{print "disk: " $3 " used / " $2 " (" $5 ")"}'