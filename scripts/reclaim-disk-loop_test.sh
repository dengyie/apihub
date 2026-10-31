#!/usr/bin/env bash
#
# Tests for the log rotation in reclaim-disk-loop.sh.
#
# The loop is the only writer of /data/new-api/logs/reclaim-disk.log and nothing
# else on the host prunes it, so "rotate correctly" is the difference between a
# bounded file and a maintenance job that slowly fills the disk it is meant to
# protect. The interesting cases are the ones a hand check misses: the size read
# that silently never fires, the archive that gets truncated when compression
# fails, and the stale second generation left behind when the platform changes.
#
# Nothing here touches the real log, supervisor, or /data.
#
# Run: bash scripts/reclaim-disk-loop_test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# The loop reads these at source time, so they must be set before sourcing.
export RECLAIM_LOG="$WORK/reclaim-disk.log"
export MAX_LOG_BYTES=1000
export RECLAIM_LOOP_LIB=1
# shellcheck source=reclaim-disk-loop.sh
source "$SCRIPT_DIR/reclaim-disk-loop.sh"

PASS=0
FAIL=0

ok()  { PASS=$((PASS + 1)); printf '  ok   %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); printf '  FAIL %s\n     %s\n' "$1" "${2:-}"; }
check(){ if [[ $2 == "$3" ]]; then ok "$1"; else bad "$1" "expected [$3], got [$2]"; fi; }

# Fill the log with $1 bytes of a repeated character, so the archive content can
# be identified later.
fill() { head -c "$1" /dev/zero | tr '\0' "$2" >"$RECLAIM_LOG"; }
# Missing files report a sentinel rather than a shell error, so a broken
# assertion reads as one line of expectation-vs-reality instead of noise.
size_of() { [[ -f $1 ]] && { wc -c <"$1" | tr -d '[:space:]'; } || echo missing; }
archive_content() {
  [[ -s "$RECLAIM_LOG.1.gz" ]] && gzip -dc "$RECLAIM_LOG.1.gz" || echo missing
}
reset_logs() { rm -f "$RECLAIM_LOG" "$RECLAIM_LOG.1" "$RECLAIM_LOG.1.gz" "$RECLAIM_LOG.rotating"; }

two_generations() {
  local files
  files=$(ls "$WORK" 2>/dev/null | grep -c '^reclaim-disk\.log')
  echo "$files"
}

echo "rotate_log"

# A log below the threshold must be left completely alone: no truncation, no
# archive, no scratch file.
reset_logs
printf 'small\n' >"$RECLAIM_LOG"
rotate_log
check "a log under the cap is not rotated" "$(cat "$RECLAIM_LOG")" "small"
check "and no archive is created" "$(ls "$WORK" | grep -c 'reclaim-disk\.log\.')" "0"

# Crossing the threshold: the live log is emptied and the old content is
# readable from the archive.
reset_logs
fill 5000 x
rotate_log
check "an oversized log is emptied" "$(size_of "$RECLAIM_LOG")" "0"
archive_content >"$WORK/unpacked" 2>/dev/null || true
check "its content lands in the archive" "$(size_of "$WORK/unpacked")" "5000"
check "exactly one archive generation exists" "$(two_generations)" "2"

# Rotating twice must replace the archive, not accumulate: one live file plus
# one archive, with the newest content.
reset_logs
fill 5000 x
rotate_log
fill 3000 y
rotate_log
check "a second rotation still leaves one generation" "$(two_generations)" "2"
check "the archive holds the newest content" "$(archive_content | tr -d 'y')" ""

# A missing log is normal (first run after a manual clean) and must stay silent:
# the loop redirects its own stderr, so anything printed here would be noise.
reset_logs
err=$(rotate_log 2>&1)
check "a missing log does not rotate" "$(ls "$WORK" | grep -c 'reclaim-disk\.log\.')" "0"
check "and stays quiet" "$err" ""

# And it must not create one: a rotate_log that opens the file to measure it
# would leave an empty log behind before the loop has written anything.
reset_logs
rotate_log
check "a missing log is not created" "$([[ -e $RECLAIM_LOG ]] && echo present || echo gone)" "gone"

# Compression failure must not truncate: the log is moved aside as a plain
# archive so the content survives and the file is still bounded.
reset_logs
fill 5000 z
(
  gzip() { return 1; }
  export -f gzip
  rotate_log
)
check "a failed compress keeps the content" "$(size_of "$RECLAIM_LOG.1")" "5000"
check "and does not leave an empty archive" "$(ls "$WORK" | grep -c 'reclaim-disk\.log\.1\.gz')" "0"
check "and the live log is gone rather than truncated" "$([[ -e $RECLAIM_LOG ]] && echo present || echo gone)" "gone"

# The fallback archive from a previous failed compress must not survive a later
# successful one, or the host would end up with two generations: a stale
# uncompressed .1 next to a current .1.gz.
reset_logs
fill 5000 a
(
  gzip() { return 1; }
  export -f gzip
  rotate_log
)
check "a failed compress leaves a plain archive" "$([[ -f $RECLAIM_LOG.1 ]] && echo present || echo gone)" "present"
fill 2000 b
rotate_log
check "a later rotation removes the stale plain archive" \
  "$([[ -e $RECLAIM_LOG.1 ]] && echo present || echo gone)" "gone"
check "and archives the newer content instead" "$(archive_content | tr -d 'b')" ""
check "leaving one generation" "$(two_generations)" "2"

# Rotation is driven by the configured cap, not a hardcoded size.
reset_logs
MAX_LOG_BYTES=100
export MAX_LOG_BYTES
fill 200 q
rotate_log
check "a smaller cap rotates a smaller log" "$(size_of "$RECLAIM_LOG")" "0"
unset MAX_LOG_BYTES
export MAX_LOG_BYTES

echo
printf 'passed %d, failed %d\n' "$PASS" "$FAIL"
(( FAIL == 0 ))
