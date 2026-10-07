#!/usr/bin/env bash
#
# apihub zero-downtime deploy.
#
# supervisord cannot do "start the new process before stopping the old one":
# `supervisorctl restart` tears the listener down first, and every request that
# arrives in that window is refused. This script inverts the order using
# SO_REUSEPORT: the incoming process binds the same port while the outgoing one
# is still serving, so the kernel always has at least one socket accepting.
#
# Two supervisor programs, `apihub-blue` and `apihub-green`, both point at the
# same binary. Exactly one is RUNNING; the other is STOPPED. A deploy starts the
# idle slot, waits until it is genuinely in the socket group, and only then
# drains the slot that was serving.
#
# IMPORTANT -- supervisord restarts any program whose config file changed when
# `supervisorctl update` runs. Rewriting the config of the slot that is
# currently serving would therefore restart it and drop in-flight requests. So
# this script only ever writes the config of a slot that is NOT running: the
# incoming slot is written before it starts, and the retired slot is rewritten
# afterwards, once it has stopped. Both end up identical again, ready for the
# next deploy.
#
# Usage:
#   deploy.sh --binary <path/to/new-api> --version <expected-version>
#   deploy.sh --bootstrap --binary <path> --version <v>   # first handover only
#   deploy.sh --status
#   deploy.sh --rollback

set -Eeuo pipefail

# ---------------------------------------------------------------------------
# Configuration. Override via environment.
# ---------------------------------------------------------------------------
APP_ROOT="${APP_ROOT:-/tmp/mnt/new-api}"
BIN_DIR="${BIN_DIR:-$APP_ROOT/bin}"
SUPERVISOR_CONF_DIR="${SUPERVISOR_CONF_DIR:-/etc/supervisor/conf.d}"
BACKUP_DIR="${BACKUP_DIR:-$APP_ROOT/backups}"
LOG_DIR="${LOG_DIR:-$APP_ROOT/logs}"
STATE_FILE="${STATE_FILE:-$APP_ROOT/.apihub-deploy-state}"
LOCK_FILE="${LOCK_FILE:-$APP_ROOT/.apihub-deploy.lock}"
PORT="${PORT:-3000}"
READY_TIMEOUT="${READY_TIMEOUT:-90}"
SLOTS=(apihub-blue apihub-green)

EXPECTED_VERSION=""
NEW_BINARY=""
BOOTSTRAP=0
MODE="deploy"
BACKUP_PATH=""
ACTIVE_SLOT=""
NEW_SLOT=""

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
log()  { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }
warn() { printf '[%s] WARN: %s\n' "$(date '+%F %T')" "$*" >&2; }
die()  { printf '[%s] FATAL: %s\n' "$(date '+%F %T')" "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary)    NEW_BINARY="${2:-}"; shift 2 ;;
    --version)   EXPECTED_VERSION="${2:-}"; shift 2 ;;
    --bootstrap) BOOTSTRAP=1; shift ;;
    --status)    MODE="status"; shift ;;
    --rollback)  MODE="rollback"; shift ;;
    -h|--help)   sed -n '2,26p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

if [[ $MODE == deploy ]]; then
  [[ -n $NEW_BINARY ]] || die "--binary is required"
  [[ -f $NEW_BINARY ]] || die "binary not found: $NEW_BINARY"
  [[ -n $EXPECTED_VERSION ]] || die "--version is required"
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
need_root() { [[ $(id -u) -eq 0 ]] || die "must run as root (supervisord and $APP_ROOT are root-owned)"; }
need_cmd()  { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

sup() { supervisorctl "$@"; }

slot_state() { sup status "$1" 2>/dev/null | awk '{print $2}'; }

slot_pid() { sup status "$1" 2>/dev/null | sed -n 's/.*pid \([0-9][0-9]*\).*/\1/p'; }

running_slot() {
  local slot
  for slot in "${SLOTS[@]}"; do
    [[ $(slot_state "$slot") == "RUNNING" ]] && { echo "$slot"; return 0; }
  done
  return 1
}

idle_slot() {
  local slot
  for slot in "${SLOTS[@]}"; do
    [[ $slot != "$1" ]] && { echo "$slot"; return 0; }
  done
  die "could not determine the idle slot (active='$1')"
}

# The legacy single-process program that this script supersedes.
LEGACY_PROGRAM="new-api"
legacy_running() { [[ $(slot_state "$LEGACY_PROGRAM") == "RUNNING" ]]; }

# A process that came up healthy on /api/status might still have bound without
# SO_REUSEPORT. The kernel is the authority here: the new pid must show up as a
# listener on the port, which only happens inside a reuseport group.
#
# Teaches: ss is absent on some hosts and netstat on others, so try both. If
# neither can report socket ownership the check fails closed, because a
# readiness check that cannot fail is worse than no readiness check at all.
reuseport_group_ok() {
  local pid="$1" port_hex
  port_hex="$(printf '%04X' "$PORT")"

  if command -v ss >/dev/null 2>&1; then
    ss -ltnp 2>/dev/null | grep -q "pid=$pid," && return 0
    return 1
  fi

  if command -v netstat >/dev/null 2>&1; then
    # netstat prints the port in decimal, and the owning process as "pid/name".
    netstat -ltnp 2>/dev/null | awk -v p="$pid" -v pp=":$PORT" '
      $4 ~ pp"$" && $NF ~ ("^" p "/") { found = 1 }
      END { exit !found }' && return 0
    return 1
  fi

  # Neither tool available: read the socket table directly and match the inode
  # against the process's open file descriptors. Here the port is hexadecimal and
  # the state column ($4) must be 0A for LISTEN -- that filter also excludes the
  # ESTABLISHED rows that share the same local port.
  local inode
  inode="$(awk -v ph=":$port_hex" 'NR>1 && $2 ~ ph"$" && $4=="0A" {print $10; exit}' \
    /proc/net/tcp /proc/net/tcp6 2>/dev/null)"
  [[ -n $inode ]] || return 1
  ls -l /proc/"$pid"/fd 2>/dev/null | grep -q "socket:\[$inode\]"
}

check_reuseport_logged() {
  local slot="$1"
  if grep -qs 'SO_REUSEPORT enabled' "$LOG_DIR/$slot.out.log" "$LOG_DIR/$slot.err.log"; then
    log "$slot: SO_REUSEPORT confirmed in the process log"
    return 0
  fi
  warn "$slot: no SO_REUSEPORT log line -- this build may predate reuseport support"
  return 1
}

# ---------------------------------------------------------------------------
# Supervisor configuration
#
# The environment line is copied verbatim from the existing production config so
# that tebi-specific values (SESSION_SECRET, proxy, SQLITE_PATH) survive the
# migration untouched. Nothing here rewrites those values -- only the program
# name, the log file, and two appended variables.
# ---------------------------------------------------------------------------
source_environment() {
  local conf="$SUPERVISOR_CONF_DIR/$LEGACY_PROGRAM.conf"
  [[ -f $conf ]] || die "cannot find $conf; refusing to guess the production environment"

  local line
  line="$(grep -E '^environment=' "$conf" | head -1)"
  [[ -n $line ]] || die "$conf has no environment= line"
  [[ $line == *SESSION_SECRET* ]] || warn "$conf has no SESSION_SECRET; sessions will not survive the deploy"

  # supervisord accepts KEY=VALUE pairs separated by commas, where a quoted
  # value ends at its closing quote. The captured line ends with that quote, so
  # drop it before appending further pairs -- otherwise the last variable of the
  # original list ends up with a doubled quote and is mis-parsed.
  line="${line#environment=}"
  line="${line%\"}"
  ENVIRONMENT_LINE="environment=${line}\",APIHUB_REUSEPORT=1,VERSION=\"${EXPECTED_VERSION}\""
}

# write_slot_conf <slot> <autostart:0|1>
#
# Only ever call this for a slot that is not currently RUNNING.
write_slot_conf() {
  local slot="$1" autostart="$2" conf="$SUPERVISOR_CONF_DIR/$1.conf"
  cat >"$conf" <<EOF
; Generated by scripts/deploy.sh -- do not edit by hand.
; One of these two slots is live at any time; deploy.sh switches which.
; Both bind the same port with SO_REUSEPORT, so during a handover the kernel
; spreads new connections across both while each drains its own in-flight work.
[program:$slot]
directory=$BIN_DIR
command=$BIN_DIR/new-api --port $PORT
user=root
autostart=$autostart
autorestart=true
stopsignal=TERM
; Must exceed the application's own SHUTDOWN_TIMEOUT_SECONDS (default 120) so
; supervisord does not SIGKILL a process still draining long-lived SSE streams.
stopwaitsecs=150
stopasgroup=true
killasgroup=true
environment=$ENVIRONMENT_LINE
redirect_stderr=true
stdout_logfile=$LOG_DIR/$slot.out.log
stderr_logfile=$LOG_DIR/$slot.err.log
stdout_logfile_maxbytes=10MB
stdout_logfile_backups=5
stderr_logfile_maxbytes=10MB
stderr_logfile_backups=5
EOF
  log "wrote $conf"
}

# apply_supervisor_conf -- reload. Safe only when no RUNNING slot's config
# changed, because supervisord restarts programs whose config it reloads.
apply_supervisor_conf() {
  need_cmd supervisorctl
  sup reread 2>&1 | sed 's/^/  reread: /' || true
  sup update >/dev/null 2>&1 || true
  local s
  for s in "${SLOTS[@]}"; do
    log "$s -> $(slot_state "$s" || echo UNKNOWN)$(slot_pid "$s" >/dev/null 2>&1 && echo " pid=$(slot_pid "$s")")"
  done
}

# ---------------------------------------------------------------------------
# Backup
#
# Taken before the binary is touched so a bad build can be rolled back in
# seconds without a rebuild. The database is copied through sqlite3's online
# backup API where available, since copying the file while it is being written
# can capture a torn page.
# ---------------------------------------------------------------------------
do_backup() {
  local stamp dest f
  stamp="$(date '+%Y%m%d-%H%M%S')"
  dest="$BACKUP_DIR/$stamp"
  mkdir -p "$dest"
  BACKUP_PATH="$dest"

  [[ -f $BIN_DIR/new-api ]] && cp -a "$BIN_DIR/new-api" "$dest/new-api.prev"
  [[ -f $STATE_FILE ]] && cp -a "$STATE_FILE" "$dest/previous-backup-path"

  for f in "$SUPERVISOR_CONF_DIR/$LEGACY_PROGRAM.conf" "$SUPERVISOR_CONF_DIR"/apihub-*.conf; do
    [[ -f $f ]] && cp -a "$f" "$dest/$(basename "$f")"
  done

  local db="$APP_ROOT/data/new-api.db"
  if [[ -f $db ]]; then
    if command -v sqlite3 >/dev/null 2>&1; then
      sqlite3 "$db" ".backup '$dest/new-api.db'" && log "database snapshotted via sqlite3 .backup"
    else
      cp -a "$db" "$dest/new-api.db"
      warn "sqlite3 not found; took a plain database copy"
    fi
  fi

  # Timestamped config snapshot, per the standing rule that config changes are
  # backed up on the persistent volume before anything is modified.
  [[ -f "$APP_ROOT/config.yaml" ]] && cp -a "$APP_ROOT/config.yaml" "$dest/config.yaml.bak_$stamp"

  log "backup complete: $dest"
}

# ---------------------------------------------------------------------------
# Readiness
#
# Both slots share one port, so a 200 from /api/status proves nothing on its
# own -- the response may have come from the process being retired. Readiness
# therefore requires the new pid to be a member of the listening socket group
# AND to have answered at least two probes. Once the old slot has been drained
# the same check runs again against the survivor.
# ---------------------------------------------------------------------------
wait_ready() {
  local slot="$1" pid="$2" deadline body ok=0 attempts=0
  deadline=$(( $(date +%s) + READY_TIMEOUT ))

  while (( $(date +%s) < deadline )); do
    attempts=$((attempts + 1))

    if ! kill -0 "$pid" 2>/dev/null; then
      warn "$slot (pid $pid) exited during startup. Recent log lines:"
      tail -20 "$LOG_DIR/$slot.err.log" >&2 2>/dev/null || true
      tail -20 "$LOG_DIR/$slot.out.log" >&2 2>/dev/null || true
      return 1
    fi

    if reuseport_group_ok "$pid"; then
      body="$(curl -fsS --max-time 5 "http://127.0.0.1:$PORT/api/status" 2>/dev/null || true)"
      if [[ -n $body ]]; then
        ok=$((ok + 1))
        if (( ok >= 2 )); then
          log "$slot (pid $pid) is in the socket group and answered $ok/$attempts probes"
          return 0
        fi
      fi
    fi
    sleep 1
  done

  warn "$slot did not become ready within ${READY_TIMEOUT}s (answered $ok of $attempts probes)"
  return 1
}

# ---------------------------------------------------------------------------
# Rollback
# ---------------------------------------------------------------------------
do_rollback() {
  need_root
  [[ -f $STATE_FILE ]] || die "no $STATE_FILE; nothing to roll back to"

  local backup prev_slot prev_version slot autostart
  backup="$(cat "$STATE_FILE")"
  [[ -d $backup ]] || die "recorded backup $backup no longer exists"
  log "rolling back to $backup"

  for slot in "${SLOTS[@]}"; do sup stop "$slot" >/dev/null 2>&1 || true; done

  [[ -f $backup/new-api.prev ]] || die "backup $backup has no previous binary"
  cp -a "$backup/new-api.prev" "$BIN_DIR/new-api"
  chmod +x "$BIN_DIR/new-api"
  log "restored the previous binary"

  prev_slot="$(cat "$backup/deploy-state.slot" 2>/dev/null || echo apihub-blue)"
  prev_slot="${prev_slot//[$'\n\r']/}"
  prev_version="$(cat "$backup/deploy-state.version" 2>/dev/null || true)"
  [[ -n $prev_version ]] || die "backup $backup does not record the previous version"
  EXPECTED_VERSION="$prev_version"

  # Both slots are stopped now, so rewriting both configs is safe.
  source_environment
  for slot in "${SLOTS[@]}"; do
    autostart=0
    [[ $slot == "$prev_slot" ]] && autostart=1
    write_slot_conf "$slot" "$autostart"
  done
  apply_supervisor_conf

  sup start "$prev_slot" || die "failed to restart $prev_slot"
  log "$prev_slot restarted on version $EXPECTED_VERSION; verifying"
  wait_ready "$prev_slot" "$(slot_pid "$prev_slot")" || die "rollback did not come up cleanly; inspect $LOG_DIR/$prev_slot.err.log"
  log "rollback complete"
}

show_status() {
  log "supervisor slots:"
  sup status "${SLOTS[@]}" 2>/dev/null || true
  log ""
  log "legacy program '$LEGACY_PROGRAM': $(slot_state "$LEGACY_PROGRAM" || echo UNTRACKED)"
  log "live binary: $(stat -c '%y %s bytes' "$BIN_DIR/new-api" 2>/dev/null || echo 'not found')"
  log "binary sha256: $(sha256sum "$BIN_DIR/new-api" 2>/dev/null | cut -c1-16 || echo n/a)"
  if [[ -f $STATE_FILE ]]; then
    log "last deploy backup: $(cat "$STATE_FILE")"
  fi
  if [[ -f $APP_ROOT/data/loadbalancer.yaml ]]; then
    log "loadbalancer.yaml modified: $(stat -c '%y' "$APP_ROOT/data/loadbalancer.yaml")"
  fi
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
  need_root

  case "$MODE" in
    status)   show_status; return 0 ;;
    rollback) do_rollback; return 0 ;;
  esac

  need_cmd curl
  need_cmd supervisorctl
  need_cmd flock
  mkdir -p "$BACKUP_DIR" "$LOG_DIR"

  # One deploy at a time: two overlapping runs would race over which slot is
  # live and could stop the wrong process.
  exec 9>"$LOCK_FILE"
  flock -n 9 || die "another deploy is already running ($LOCK_FILE)"

  do_backup

  # Install the new binary before touching any process. A running process keeps
  # its open inode, so replacing the file does not disturb the serving slot.
  local staged="$BIN_DIR/new-api.new"
  install -m 0755 "$NEW_BINARY" "$staged"
  mv -f "$staged" "$BIN_DIR/new-api"
  log "installed binary ($(stat -c %s "$BIN_DIR/new-api") bytes, sha256 $(sha256sum "$BIN_DIR/new-api" | cut -c1-16))"

  if legacy_running; then
    if (( BOOTSTRAP == 0 )); then
      die "legacy program '$LEGACY_PROGRAM' is still serving and predates SO_REUSEPORT support.
       One restart with a brief connection gap is unavoidable for this first handover.
       Re-run with --bootstrap when you are ready to accept that."
    fi

    # Bootstrap. The incumbent cannot share the port, so exactly one short
    # window has nothing listening. Stop it before the slots are created:
    # apihub-blue is autostart, and starting it while the legacy process still
    # owns the port would crash-loop it on EADDRINUSE.
    log "BOOTSTRAP: stopping $LEGACY_PROGRAM (expect a short connection gap)"
    sup stop "$LEGACY_PROGRAM"

    EXPECTED_VERSION="$EXPECTED_VERSION"
    source_environment
    write_slot_conf apihub-blue 1
    write_slot_conf apihub-green 0
    apply_supervisor_conf

    local pid
    pid="$(slot_pid apihub-blue)"
    [[ -n $pid ]] || die "apihub-blue did not start; check $LOG_DIR/apihub-blue.err.log"
    if wait_ready apihub-blue "$pid"; then
      check_reuseport_logged apihub-blue || true
      printf '%s' "$BACKUP_PATH" >"$STATE_FILE"
      log "bootstrap complete; apihub-blue is live on version $EXPECTED_VERSION"
      log "subsequent deploys are zero-downtime"
      return 0
    fi
    sup stop apihub-blue || true
    die "bootstrap failed; nothing is serving. Restore manually from $BACKUP_PATH"
  fi

  ACTIVE_SLOT="$(running_slot)" || die "neither slot is RUNNING and no legacy program is serving.
       Traffic is already down; start one manually and inspect $BACKUP_PATH"
  NEW_SLOT="$(idle_slot "$ACTIVE_SLOT")"

  local active_pid
  active_pid="$(slot_pid "$ACTIVE_SLOT")"
  log "active slot: $ACTIVE_SLOT (pid $active_pid) -> handing over to $NEW_SLOT"
  check_reuseport_logged "$ACTIVE_SLOT" \
    || warn "$ACTIVE_SLOT may not support reuseport; a first --bootstrap deploy was probably skipped"

  # Only the idle slot's config is touched, so `update` cannot restart the
  # process that is currently serving traffic.
  source_environment
  write_slot_conf "$NEW_SLOT" 0
  apply_supervisor_conf

  sup start "$NEW_SLOT"
  local new_pid
  new_pid="$(slot_pid "$NEW_SLOT")"
  [[ -n $new_pid ]] || { sup stop "$NEW_SLOT" || true; die "$NEW_SLOT failed to start"; }

  if ! wait_ready "$NEW_SLOT" "$new_pid"; then
    warn "$NEW_SLOT failed readiness; aborting the handover and leaving $ACTIVE_SLOT serving"
    sup stop "$NEW_SLOT" || true
    printf '%s' "$BACKUP_PATH" >"$STATE_FILE"
    die "deploy aborted; $ACTIVE_SLOT is still serving"
  fi
  check_reuseport_logged "$NEW_SLOT" || warn "continuing, but verify $NEW_SLOT really shares the port"

  # Record the outgoing slot before touching it, so --rollback knows where to
  # return to.
  printf '%s' "$ACTIVE_SLOT" >"$BACKUP_PATH/deploy-state.slot"
  printf '%s' "$EXPECTED_VERSION" >"$BACKUP_PATH/deploy-state.version"

  # The new slot is in the group and answering. Drain the old one: SIGTERM
  # triggers the application's own graceful shutdown, which waits for in-flight
  # requests, including long-lived SSE streams, up to SHUTDOWN_TIMEOUT_SECONDS.
  local drain_start; drain_start=$(date +%s)
  log "retiring $ACTIVE_SLOT (pid $active_pid); draining in-flight requests"
  sup stop "$ACTIVE_SLOT" || warn "supervisorctl stop $ACTIVE_SLOT returned non-zero"
  log "$ACTIVE_SLOT drained after $(( $(date +%s) - drain_start ))s"

  # The retired slot is stopped, so its config can now be brought in line with
  # the new version for the next deploy.
  write_slot_conf "$ACTIVE_SLOT" 0
  apply_supervisor_conf

  if ! wait_ready "$NEW_SLOT" "$(slot_pid "$NEW_SLOT")"; then
    die "post-handover health check failed on $NEW_SLOT. Previous backup: $BACKUP_PATH"
  fi

  printf '%s' "$BACKUP_PATH" >"$STATE_FILE"
  log "handover complete; $NEW_SLOT serving version $EXPECTED_VERSION"
  sup status "${SLOTS[@]}" 2>/dev/null || true
}

main "$@"