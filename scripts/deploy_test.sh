#!/usr/bin/env bash
#
# Tests for the frontend half of deploy.sh.
#
# These exercise the functions that decide whether a frontend bundle is allowed
# to go anywhere near a running process, plus the config plumbing that binds it
# to a slot. Nothing here touches supervisor, the port, or a real deployment --
# the assertions are all about files in a temp directory.
#
# Run: bash scripts/deploy_test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_SH_LIB=1
export DEPLOY_SH_LIB
# shellcheck source=deploy.sh
source "$SCRIPT_DIR/deploy.sh"

# deploy.sh runs under `set -Eeuo pipefail`, and sourcing it turns that on for
# this shell too. These assertions deliberately invoke functions that fail, so
# errexit has to go -- otherwise the first expected failure ends the run.
set +e +u
set -o pipefail

PASS=0
FAIL=0

ok()   { PASS=$((PASS + 1)); printf '  ok   %s\n' "$1"; }
bad()  { FAIL=$((FAIL + 1)); printf '  FAIL %s\n     %s\n' "$1" "${2:-}"; }
check(){ if [[ $2 == "$3" ]]; then ok "$1"; else bad "$1" "expected [$3], got [$2]"; fi; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

APP_ROOT="$WORK/app"
WEB_ROOT="$APP_ROOT/web"
WEB_LINK="$WEB_ROOT/current"
SUPERVISOR_CONF_DIR="$WORK/conf.d"
BIN_DIR="$APP_ROOT/bin"
mkdir -p "$WEB_ROOT" "$SUPERVISOR_CONF_DIR" "$BIN_DIR"

# A realistic frontend: one entry document plus a content-hashed bundle. The
# hashed names are what the fingerprint check keys on.
make_bundle() {
  local dir="$1" tag="$2" i
  mkdir -p "$dir/static/js"
  {
    printf '<!doctype html><html><head><title>APIHub</title>\n'
    printf '<link rel="stylesheet" href="/static/css/%s.css">\n' "$tag"
    printf '<script type="module" src="/static/js/%s.js"></script>\n' "$tag"
    printf '</head><body><div id="root"></div>\n'
    printf '<!--umami-->\n<!--Google Analytics-->\n</body></html>\n'
  } >"$dir/index.html"
  for i in $(seq 1 12); do printf '/* %s %d */\n' "$tag" "$i" >"$dir/static/js/$tag-$i.js"; done
}

echo "verify_web_bundle"
make_bundle "$WORK/good" "aaa1"
verify_web_bundle "$WORK/good" >/dev/null 2>&1 && ok "accepts a well-formed bundle" || bad "accepts a well-formed bundle"

make_bundle "$WORK/small-index" "bbb1"
printf 'x' >"$WORK/small-index/index.html"
out="$(verify_web_bundle "$WORK/small-index" 2>&1)" && bad "rejects a tiny index.html" || true
grep -q "need >=" <<<"$out" && ok "rejects a tiny index.html" || bad "rejects a tiny index.html" "$out"

make_bundle "$WORK/few-static" "ccc1"
rm -f "$WORK/few-static"/static/js/*.js
out="$(verify_web_bundle "$WORK/few-static" 2>&1)"
grep -q "static tree has only" <<<"$out" && ok "rejects too few static files" || bad "rejects too few static files" "$out"

mkdir -p "$WORK/no-index/static/js"
out="$(verify_web_bundle "$WORK/no-index" 2>&1)"
grep -q "no index.html" <<<"$out" && ok "rejects a bundle with no index.html" || bad "rejects a bundle with no index.html" "$out"

out="$(verify_web_bundle "$WORK/does-not-exist" 2>&1)"
grep -q "does not exist" <<<"$out" && ok "reports a missing directory" || bad "reports a missing directory" "$out"

echo "web_fingerprint"
fp="$(web_fingerprint "$WORK/good")"
grep -q "/static/js/aaa1.js" <<<"$fp" && grep -q "/static/css/aaa1.css" <<<"$fp" \
  && ok "extracts the hashed asset URLs" || bad "extracts the hashed asset URLs" "$fp"
[[ $(web_fingerprint "$WORK/good") != "$(web_fingerprint "$WORK/small-index")" || $? == 0 ]] && true
grep -q "aaa1" <<<"$fp" && ! grep -q "bbb1" <<<"$fp" && ok "does not pick up assets from other builds" \
  || bad "does not pick up assets from other builds" "$fp"

echo "stage_web_bundle"
mkdir -p "$WORK/pack"
make_bundle "$WORK/pack" "ddd1"
tar -czf "$WORK/web.tar.gz" -C "$WORK/pack" .
EXPECTED_VERSION="feat/web-4f2a"
STAGED_WEB_DIR=""
stage_web_bundle "$WORK/web.tar.gz" >/dev/null 2>&1
check "stages into a per-version directory" "$STAGED_WEB_DIR" "$WEB_ROOT/feat_web-4f2a"
[[ -f "$STAGED_WEB_DIR/index.html" ]] && ok "staged bundle has index.html at its root" || bad "staged bundle has index.html at its root"
[[ $(static_file_count "$STAGED_WEB_DIR") -ge 10 ]] && ok "staged bundle kept every asset" || bad "staged bundle kept every asset"

# A tarball wrapped in a single top-level directory, which is what
# `tar -czf x.tgz web/dist` produces.
mkdir -p "$WORK/wrapped"
make_bundle "$WORK/wrapped" "eee1"
tar -czf "$WORK/wrapped.tar.gz" -C "$WORK/wrapped" .
EXPECTED_VERSION="wrapped"
STAGED_WEB_DIR=""
stage_web_bundle "$WORK/wrapped.tar.gz" >/dev/null 2>&1
[[ -f "$STAGED_WEB_DIR/index.html" ]] && ok "unwraps a single top-level directory" || bad "unwraps a single top-level directory" "$STAGED_WEB_DIR"

# A member that escapes the target directory must be refused outright. Built
# with python rather than tar's --transform, which GNU tar has and BSD tar (any
# macOS) does not.
python3 - "$WORK/evil.tar.gz" <<'PY'
import io, sys, tarfile
with tarfile.open(sys.argv[1], "w:gz") as tf:
    data = b"pwned\n"
    info = tarfile.TarInfo("../../escaped.txt")
    info.size = len(data)
    tf.addfile(info, io.BytesIO(data))
PY
EXPECTED_VERSION="evil"
out="$(stage_web_bundle "$WORK/evil.tar.gz" 2>&1)"
grep -q "parent-relative\|absolute" <<<"$out" && ok "refuses a tarball that escapes WEB_ROOT" || bad "refuses a tarball that escapes WEB_ROOT" "$out"
[[ ! -e $WEB_ROOT/escaped.txt && ! -e $WORK/escaped.txt ]] && ok "nothing was unpacked outside WEB_ROOT" || bad "nothing was unpacked outside WEB_ROOT"

# A bundle that fails validation must not reach WEB_ROOT at all.
make_bundle "$WORK/pack" "fff1"
printf 'x' >"$WORK/pack/index.html"
tar -czf "$WORK/bad.tar.gz" -C "$WORK/pack" .
EXPECTED_VERSION="badbundle"
out="$(stage_web_bundle "$WORK/bad.tar.gz" 2>&1 || true)"
grep -q "need >=" <<<"$out" && ok "aborts on a bundle that fails validation" || bad "aborts on a bundle that fails validation" "$out"
[[ ! -e $WEB_ROOT/badbundle ]] && ok "a rejected bundle is left on disk nowhere" || bad "a rejected bundle is left on disk nowhere"

echo "source_environment / write_slot_conf"
LEGACY_PROGRAM="new-api"
cat >"$SUPERVISOR_CONF_DIR/new-api.conf" <<EOF
[program:new-api]
environment=SESSION_SECRET="s3cr3t-value",SQLITE_PATH="/tmp/mnt/new-api/data/new-api.db",APIHUB_STATIC_DIR="$WEB_ROOT/stale-dir"
EOF
EXPECTED_VERSION="abc123"
# Called directly, not in a command substitution: source_environment sets the
# global ENVIRONMENT_LINE, and a $( ) would assign it inside a subshell where
# the assertions below could not see it.
source_environment >/dev/null 2>&1
grep -q 'APIHUB_STATIC_DIR' <<<"$ENVIRONMENT_LINE" && bad "drops an inherited APIHUB_STATIC_DIR" "$ENVIRONMENT_LINE" \
  || ok "drops an inherited APIHUB_STATIC_DIR"
grep -q 'SQLITE_PATH="/tmp/mnt/new-api/data/new-api.db",' <<<"$ENVIRONMENT_LINE" \
  && ok "preserves the surrounding environment pairs verbatim" || bad "preserves the surrounding environment pairs verbatim" "$ENVIRONMENT_LINE"
grep -q 'SESSION_SECRET="s3cr3t-value"' <<<"$ENVIRONMENT_LINE" \
  && ok "preserves SESSION_SECRET" || bad "preserves SESSION_SECRET"
grep -q 'APIHUB_REUSEPORT=1' <<<"$ENVIRONMENT_LINE" && ok "still appends APIHUB_REUSEPORT" || bad "still appends APIHUB_REUSEPORT"

# The line must not end with a doubled quote, and an unquoted trailing variable
# must survive intact. A rehearsal caught this: a legacy config ending in
# `PORT=3998` produced `PORT=3998"` and the process then read the wrong value.
grep -q '""' <<<"$ENVIRONMENT_LINE" && bad "no doubled quotes in the assembled line" "$ENVIRONMENT_LINE" \
  || ok "no doubled quotes in the assembled line"
grep -q 'SQLITE_PATH="/tmp/mnt/new-api/data/new-api.db",APIHUB_REUSEPORT' <<<"$ENVIRONMENT_LINE" \
  && ok "a quoted final variable keeps its closing quote" || bad "a quoted final variable keeps its closing quote" "$ENVIRONMENT_LINE"

cat >"$SUPERVISOR_CONF_DIR/new-api.conf" <<EOF
[program:new-api]
environment=SESSION_SECRET="s3cr3t-value",PORT=3998,DEBUG=true
EOF
source_environment >/dev/null 2>&1
grep -q 'PORT=3998,DEBUG=true,APIHUB_REUSEPORT' <<<"$ENVIRONMENT_LINE" \
  && ok "an unquoted final variable is not given a stray quote" || bad "an unquoted final variable is not given a stray quote" "$ENVIRONMENT_LINE"
grep -q 'DEBUG=true,APIHUB_REUSEPORT' <<<"$ENVIRONMENT_LINE" \
  && ok "a bare flag survives unchanged" || bad "a bare flag survives unchanged" "$ENVIRONMENT_LINE"

# After the legacy config is gone the environment has to come from a slot.
rm -f "$SUPERVISOR_CONF_DIR/new-api.conf"
# In a subshell: source_environment ends in die, which calls exit, and a bare
# exit inside an && list would take the whole test run down with it.
( source_environment ) >/dev/null 2>&1 && bad "refuses to guess when no config exists" \
  || ok "refuses to guess when no config exists"

write_slot_conf apihub-blue 1 "$WEB_ROOT/abc123" >/dev/null
conf="$SUPERVISOR_CONF_DIR/apihub-blue.conf"
grep -q "APIHUB_STATIC_DIR=\"$WEB_ROOT/abc123\"" "$conf" && ok "binds the slot to its own frontend directory" || bad "binds the slot to its own frontend directory"
check "reads that directory back" "$(slot_static_dir apihub-blue)" "$WEB_ROOT/abc123"

# The environment line specifically: the config's comment header legitimately
# mentions the variable by name.
env_line_of() { grep -E '^environment=' "$SUPERVISOR_CONF_DIR/$1.conf"; }

write_slot_conf apihub-green 0 "" >/dev/null
! grep -q "APIHUB_STATIC_DIR" <(env_line_of apihub-green) \
  && ok "omits the variable when there is no bundle" || bad "omits the variable when there is no bundle"
check "an unbound slot reports no directory" "$(slot_static_dir apihub-green)" ""

# A path outside WEB_ROOT is not ours to serve.
write_slot_conf apihub-green 0 "/etc" >/dev/null
check "refuses to read a directory outside WEB_ROOT" "$(slot_static_dir apihub-green)" ""

# A slot config becomes the input for the next deploy once the legacy one is
# gone. The variables this script appends must therefore appear exactly once
# after a re-read -- carried over AND appended would make them twice, and the
# reusport/VERSION flags would be duplicated on every deploy from then on.
# APIHUB_STATIC_DIR is the exception: it is bound per-slot, so source_environment
# must not carry it over and write_slot_conf must add it back.
write_slot_conf apihub-blue 1 "$WEB_ROOT/abc123" >/dev/null
source_environment >/dev/null 2>&1
check "APIHUB_STATIC_DIR is not carried into the base environment" \
  "$(grep -o 'APIHUB_STATIC_DIR=' <<<"$ENVIRONMENT_LINE" | wc -l | tr -d ' ')" "0"
for v in APIHUB_REUSEPORT VERSION; do
  check "$v appears exactly once after a re-read" \
    "$(grep -o "$v=" <<<"$ENVIRONMENT_LINE" | wc -l | tr -d ' ')" "1"
done
grep -q 'SESSION_SECRET="s3cr3t-value"' <<<"$ENVIRONMENT_LINE" \
  && ok "the production environment survives a slot-config re-read" \
  || bad "the production environment survives a slot-config re-read" "$ENVIRONMENT_LINE"

echo "verify_frontend_served"
# Uses a real HTTP server on a loopback port, because the whole point of the
# check is what comes back over the wire.
PORT="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')"
FINGERPRINT_TIMEOUT=4
( cd "$WORK/good" && exec python3 -m http.server "$PORT" --bind 127.0.0.1 >/dev/null 2>&1 ) &
SRV=$!
sleep 1
verify_frontend_served apihub-test "$WORK/good" >/dev/null 2>&1 \
  && ok "accepts a page that references the deployed assets" \
  || bad "accepts a page that references the deployed assets"

verify_frontend_served apihub-test "$WORK/nonexistent-build" >/dev/null 2>&1 \
  && bad "rejects a page from a different build" \
  || ok "rejects a page from a different build"

kill "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null

# http_get must bypass any proxy. tebi runs root with a ~/.curlrc pointing at
# Clash on 127.0.0.1:7897; without -q and --noproxy, curl sends the probe to
# the proxy, which answers 502 for loopback it cannot reach -- indistinguishable
# from "not ready", which is how a healthy handover gets declared a failure.
( cd "$WORK/good" && exec python3 -m http.server "$PORT" --bind 127.0.0.1 >/dev/null 2>&1 ) &
SRV=$!
sleep 1
http_body="$(http_get /)"
grep -q "<title>" <<<"$http_body" \
  && ok "http_get reaches loopback" || bad "http_get reaches loopback" "$http_body"
mkdir -p "$WORK/fakehome"
printf 'proxy=http://127.0.0.1:1\n' > "$WORK/fakehome/.curlrc"
body="$( HOME="$WORK/fakehome" http_get / )"
[[ -n $body ]] && ok "http_get ignores a proxy set in ~/.curlrc" \
  || bad "http_get ignores a proxy set in ~/.curlrc" "empty"
kill "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null

# Nothing listening at all must fail closed, not pass vacuously.
verify_frontend_served apihub-test "$WORK/good" >/dev/null 2>&1 \
  && bad "fails when the port answers nothing" || ok "fails when the port answers nothing"

# The APP_ROOT / SUPERVISOR_CONF_DIR coupling guard. A rehearsal that overrides
# APP_ROOT but not SUPERVISOR_CONF_DIR would otherwise write the live
# production slot configs, which is exactly what happened once.
saved_root="$APP_ROOT"; saved_conf="$SUPERVISOR_CONF_DIR"
APP_ROOT=/tmp/sandbox-only; SUPERVISOR_CONF_DIR=/etc/supervisor/conf.d
out="$( ( guard_conf_dir ) 2>&1 )"
check "refuses to run against a sandbox APP_ROOT with the production conf dir" "$?" "1"
grep -q 'overwrite the production' <<<"$out" \
  && ok "the guard says what would be overwritten" || bad "the guard says what would be overwritten" "$out"
SUPERVISOR_CONF_DIR=/tmp/sandbox-conf
( guard_conf_dir ) >/dev/null 2>&1 && ok "allows an explicit sandbox conf dir" || bad "allows an explicit sandbox conf dir"
APP_ROOT=/tmp/mnt/new-api
out="$( ( guard_conf_dir ) 2>&1 )"
check "allows the production defaults" "$?" "0"
APP_ROOT="$saved_root"; SUPERVISOR_CONF_DIR="$saved_conf"

echo "prune_web_dirs"
for n in v1 v2 v3 v4 v5; do make_bundle "$WEB_ROOT/$n" "$n"; sleep 0.05; done
prune_web_dirs "$WEB_ROOT/v5" >/dev/null
[[ -d $WEB_ROOT/v5 ]] && ok "keeps the directory that is live" || bad "keeps the directory that is live"
[[ ! -e $WEB_ROOT/v1 ]] && ok "removes directories beyond the retention count" || bad "removes directories beyond the retention count"
kept=$(find "$WEB_ROOT" -mindepth 1 -maxdepth 1 -type d ! -name '.staging.*' | wc -l | tr -d ' ')
check "keeps WEB_KEEP versions" "$kept" "$WEB_KEEP"

make_bundle "$WEB_ROOT/rollbackme" "rb1"
prune_web_dirs "$WEB_ROOT/v5" "$WEB_ROOT/rollbackme" >/dev/null
[[ -d $WEB_ROOT/rollbackme ]] && ok "keeps a directory --rollback would return to" || bad "keeps a directory --rollback would return to"

echo
printf 'passed %d, failed %d\n' "$PASS" "$FAIL"
[[ $FAIL -eq 0 ]] || exit 1