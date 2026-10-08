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
# SQLITE_PATH used to sit between SESSION_SECRET and the appended block, carried
# through verbatim. It is now dropped and re-appended, so SESSION_SECRET is the
# last inherited pair; what still matters is that removing its neighbour left the
# quoting and the separator intact.
grep -q 'SESSION_SECRET="s3cr3t-value",' <<<"$ENVIRONMENT_LINE" \
  && ok "preserves the surrounding environment pairs verbatim" || bad "preserves the surrounding environment pairs verbatim" "$ENVIRONMENT_LINE"
grep -q 'SESSION_SECRET="s3cr3t-value"' <<<"$ENVIRONMENT_LINE" \
  && ok "preserves SESSION_SECRET" || bad "preserves SESSION_SECRET"
grep -q 'APIHUB_REUSEPORT=1' <<<"$ENVIRONMENT_LINE" && ok "still appends APIHUB_REUSEPORT" || bad "still appends APIHUB_REUSEPORT"

# The line must not end with a doubled quote, and an unquoted trailing variable
# must survive intact. A rehearsal caught this: a legacy config ending in
# `PORT=3998` produced `PORT=3998"` and the process then read the wrong value.
grep -q '""' <<<"$ENVIRONMENT_LINE" && bad "no doubled quotes in the assembled line" "$ENVIRONMENT_LINE" \
  || ok "no doubled quotes in the assembled line"
# SQLITE_PATH used to be the input's last variable and stayed last; it is now
# re-appended after VERSION, so the closing-quote invariant is checked against
# whichever variable actually ends the line.
grep -q 'SQLITE_PATH="/tmp/mnt/new-api/data/new-api.db"$' <<<"$ENVIRONMENT_LINE" \
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

# slot_static_dir reads a real bundle, so the directory it points at has to be a
# real bundle: this used to assert against a bare mkdir, which passed under the
# old "is it under WEB_ROOT" rule and is correctly rejected now.
make_bundle "$WEB_ROOT/abc123" "abc123"
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

# A directory that is not a frontend bundle is not ours to serve. /etc is a
# directory, so existence alone would accept it; the index.html is what tells a
# bundle apart from an arbitrary path someone left in a config.
write_slot_conf apihub-green 0 "/etc" >/dev/null
check "refuses a directory that is not a frontend bundle" "$(slot_static_dir apihub-green)" ""

# The migration case: a live slot bound to the previous root's bundle, read
# after WEB_ROOT has moved. Refusing this recorded an empty value, and
# --rollback would then have quietly restored the embedded copy instead.
make_bundle "$WORK/old-root-web/abc123" "abc123"
write_slot_conf apihub-green 0 "$WORK/old-root-web/abc123" >/dev/null
check "accepts a live bundle from the previous root" \
  "$(slot_static_dir apihub-green)" "$WORK/old-root-web/abc123"

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

echo "SQLITE_PATH ownership"
# SQLITE_PATH is inherited verbatim like every other variable, which during a
# storage migration means the incoming slot keeps using the OLD database file
# forever -- or, if it were merely appended, uses both. The deployment root has
# to own it, or DB_PATH cannot do its one job.
DB_PATH="/data/new-api/data/new-api.db"
source_environment >/dev/null 2>&1
grep -q 'SQLITE_PATH="/data/new-api/data/new-api.db"' <<<"$ENVIRONMENT_LINE" \
  && ok "SQLITE_PATH follows DB_PATH, not the previous config" \
  || bad "SQLITE_PATH follows DB_PATH, not the previous config" "$ENVIRONMENT_LINE"
check "SQLITE_PATH appears exactly once" \
  "$(grep -o 'SQLITE_PATH=' <<<"$ENVIRONMENT_LINE" | wc -l | tr -d ' ')" "1"
DB_PATH="$APP_ROOT/data/new-api.db"

echo "ensure_data_link"
# loadbalancer.Init("data/loadbalancer.yaml") is relative to BIN_DIR and nothing
# else resolves it. Without this link a relocated root boots cleanly on default
# routing policy with nothing in the log to say so.
LINK_DIR="$WORK/linktest"
rm -rf "$LINK_DIR"; mkdir -p "$LINK_DIR/bin" "$LINK_DIR/data"
BIN_DIR="$LINK_DIR/bin"; DATA_DIR="$LINK_DIR/data"
ensure_data_link >/dev/null 2>&1
check "creates the data link when it is missing" "$(readlink "$BIN_DIR/data")" "$DATA_DIR"
touch "$DATA_DIR/loadbalancer.yaml"
[[ -f "$BIN_DIR/data/loadbalancer.yaml" ]] \
  && ok "the policy file is reachable through the link" \
  || bad "the policy file is reachable through the link"
ensure_data_link >/dev/null 2>&1
check "leaves a correct link alone" "$(readlink "$BIN_DIR/data")" "$DATA_DIR"
DATA_DIR="$LINK_DIR/data-moved"; mkdir -p "$DATA_DIR"
ensure_data_link >/dev/null 2>&1
check "repoints a stale link" "$(readlink "$BIN_DIR/data")" "$DATA_DIR"
rm -f "$BIN_DIR/data"; mkdir -p "$BIN_DIR/data"
# In a subshell: the refusal goes through die(), which calls exit -- invoked at
# this level it would end the whole test run instead of failing one assertion.
( ensure_data_link ) >/dev/null 2>&1
check "refuses to replace a real directory" "$([[ -d $BIN_DIR/data && ! -L $BIN_DIR/data ]] && echo kept)" "kept"
BIN_DIR="$APP_ROOT/bin"; DATA_DIR="$APP_ROOT/data"

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

echo "wait_ready version check"
# wait_ready is the only gate that can tell "the right binary" from "a stale
# artifact that happens to serve the right frontend". Served here by a stub that
# answers /api/status with a caller-chosen version; reuseport_group_ok is stubbed
# out, because whether the pid owns the port is not what is under test.
reuseport_group_ok() { return 0; }

cat >"$WORK/status_server.py" <<'PY'
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

VERSION = sys.argv[2]


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = ('{"success":true,"data":{"version":"%s"}}' % VERSION).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
PY

serve_status_with() {
  python3 "$WORK/status_server.py" "$PORT" "$1" >/dev/null 2>&1 &
  SRV=$!
  sleep 1
}

READY_TIMEOUT=4
EXPECTED_VERSION="v29.36+goodsha"
serve_status_with "v29.36+goodsha"
wait_ready apihub-test "$$" >/dev/null 2>&1 \
  && ok "accepts a process reporting the expected version" \
  || bad "accepts a process reporting the expected version"
kill "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null

# The failure this prevents: every other gate passes -- two probes answered, the
# port answers 200, the frontend fingerprint matches -- while the wrong binary
# is live. The deploy must not be able to report success here.
serve_status_with "v29.36+stalesha"
out="$(wait_ready apihub-test "$$" 2>&1)"
grep -q "reports version 'v29.36+stalesha'" <<<"$out" \
  && ok "rejects a process reporting the wrong version" \
  || bad "rejects a process reporting the wrong version" "$out"
kill "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null

# An empty EXPECTED_VERSION must not turn the check into a false pass.
EXPECTED_VERSION=""
serve_status_with "v29.36+stalesha"
wait_ready apihub-test "$$" >/dev/null 2>&1 \
  && ok "an unset expectation accepts whatever the process reports" \
  || bad "an unset expectation accepts whatever the process reports"
kill "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null
EXPECTED_VERSION=""

serve_status_with "v29.36+fromdisk"
check "served_version reads the version out of /api/status" "$(served_version apihub-test)" "v29.36+fromdisk"
kill "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null

check "served_version reports a dead port instead of pretending" "$(served_version apihub-test)" "<not answering>"

echo "verify_binary_version"
# A stub that answers --version, standing in for the real binary. The point of
# this gate is that a build whose -X target did not resolve must never reach the
# host's bin directory -- and it cannot be caught at runtime, because InitEnv
# overwrites common.Version from the VERSION env var this script writes itself.
stub="$(mktemp -d)"
cat >"$stub/fakebinary" <<'SH'
#!/usr/bin/env bash
printf '%s\n' 'v29.36+0a1cc23b3'
SH
chmod +x "$stub/fakebinary"

verify_binary_version "$stub/fakebinary" "v29.36+0a1cc23b3" >/dev/null 2>&1 \
  && ok "accepts a binary that reports the expected version" \
  || bad "accepts a binary that reports the expected version"

out="$(verify_binary_version "$stub/fakebinary" "v29.36+0a1cc23b" 2>&1)"
grep -q "Refusing to install" <<<"$out" \
  && ok "refuses a binary stamped with a different version" \
  || bad "refuses a binary stamped with a different version" "$out"
grep -q "common.Version at its compiled-in default" <<<"$out" \
  && ok "explains an unresolved -X target rather than just failing" \
  || bad "explains an unresolved -X target rather than just failing" "$out"

# The shape a silently-unstamped build actually has: the binary runs, but
# common.Version never left its compiled-in default.
cat >"$stub/fakebinary" <<'SH'
#!/usr/bin/env bash
printf '%s\n' 'v0.0.0'
SH
chmod +x "$stub/fakebinary"
out="$(verify_binary_version "$stub/fakebinary" "v29.36+0a1cc23b3" 2>&1)"
grep -q "v0.0.0" <<<"$out" \
  && ok "catches the v0.0.0 default that a failed -X leaves behind" \
  || bad "catches the v0.0.0 default that a failed -X leaves behind" "$out"
rm -rf "$stub"

# The status report must work on BSD userland too: it is the first command an
# operator reaches for when something is broken.
printf 'x' >"$WORK/somefile"
mtime="$(file_mtime "$WORK/somefile")"
[[ -n $mtime && $mtime != unknown ]] && ok "file_mtime works on this platform" || bad "file_mtime works on this platform" "$mtime"
bundles="$(ls -1 "$WORK" 2>/dev/null | grep -v '^\.staging\.' | tr '\n' ' ')"
[[ $bundles == *somefile* ]] && ok "bundle listing works on this platform" || bad "bundle listing works on this platform" "$bundles"

# The APP_ROOT / SUPERVISOR_CONF_DIR coupling guard. A rehearsal that overrides
# APP_ROOT but never names SUPERVISOR_CONF_DIR would otherwise write the live
# production slot configs, which is exactly what happened once.
#
# The guard tests whether the variable was NAMED, so these cases set
# SUPERVISOR_CONF_DIR_EXPLICIT directly -- that is the value the guard reads, and
# setting only SUPERVISOR_CONF_DIR would leave the flag saying "not named".
saved_root="$APP_ROOT"; saved_conf="$SUPERVISOR_CONF_DIR"; saved_explicit="$SUPERVISOR_CONF_DIR_EXPLICIT"

APP_ROOT=/tmp/sandbox-only
SUPERVISOR_CONF_DIR=/etc/supervisor/conf.d; SUPERVISOR_CONF_DIR_EXPLICIT=""
out="$( ( guard_conf_dir ) 2>&1 )"
check "refuses a sandbox APP_ROOT when the conf dir was not named" "$?" "1"
grep -q 'overwrite the production' <<<"$out" \
  && ok "the guard says what would be overwritten" || bad "the guard says what would be overwritten" "$out"

# The case that made the value-based version unusable: relocating the root on
# purpose, while still managing the live slot configs where they already are.
SUPERVISOR_CONF_DIR_EXPLICIT="x"
( guard_conf_dir ) >/dev/null 2>&1 \
  && ok "allows a deliberate relocation that names the production conf dir" \
  || bad "allows a deliberate relocation that names the production conf dir"

SUPERVISOR_CONF_DIR=/tmp/sandbox-conf; SUPERVISOR_CONF_DIR_EXPLICIT="x"
( guard_conf_dir ) >/dev/null 2>&1 && ok "allows an explicit sandbox conf dir" || bad "allows an explicit sandbox conf dir"

APP_ROOT=/tmp/mnt/new-api; SUPERVISOR_CONF_DIR_EXPLICIT=""
out="$( ( guard_conf_dir ) 2>&1 )"
check "allows the production defaults" "$?" "0"

APP_ROOT="$saved_root"; SUPERVISOR_CONF_DIR="$saved_conf"; SUPERVISOR_CONF_DIR_EXPLICIT="$saved_explicit"

echo "prune_web_dirs"
for n in v1 v2 v3 v4 v5; do make_bundle "$WEB_ROOT/$n" "$n"; sleep 0.05; done
prune_web_dirs "$WEB_ROOT/v5" >/dev/null
[[ -d $WEB_ROOT/v5 ]] && ok "keeps the directory that is live" || bad "keeps the directory that is live"
[[ ! -e $WEB_ROOT/v1 ]] && ok "removes directories beyond the retention count" || bad "removes directories beyond the retention count"
kept=$(find "$WEB_ROOT" -mindepth 1 -maxdepth 1 -type d ! -name '.staging.*' | wc -l | tr -d ' ')
# WEB_KEEP counts unprotected directories only. v5 is protected, so the total is
# one higher -- and it used to come out one LOWER, because the broken guard let
# v5 spend a slot and this assertion was quietly pinning that bug in place.
check "keeps WEB_KEEP versions plus the protected one" "$kept" "$((WEB_KEEP + 1))"

make_bundle "$WEB_ROOT/rollbackme" "rb1"
prune_web_dirs "$WEB_ROOT/v5" "$WEB_ROOT/rollbackme" >/dev/null
[[ -d $WEB_ROOT/rollbackme ]] && ok "keeps a directory --rollback would return to" || bad "keeps a directory --rollback would return to"

# The two assertions above passed for the wrong reason. `ls -dt` returns the
# newest first, and the protected directory was also the newest, so the
# retention counter never reached it and the broken guard was never exercised.
# Production does not line up that way: a bundle is unpacked from a tarball
# whose entries carry the build machine's mtime, so the directory that most
# needs protecting can be the OLDEST on disk. That is exactly how the live
# slot's bundle got deleted on the first real deployment. Pin the order so the
# guard is the only thing keeping these directories alive.
rm -rf "${WEB_ROOT:?}"/*; mkdir -p "$WEB_ROOT"
for n in v1 v2 v3 v4 v5; do make_bundle "$WEB_ROOT/$n" "$n"; done
# Distinct mtimes, so "oldest first out" is a fact and not a tie the filesystem
# happens to break. v5 is the live directory and v1 the oldest, neither of which
# a tarball-unpacked bundle would necessarily be.
touch -t 202601010101 "$WEB_ROOT/v1"
touch -t 202601020202 "$WEB_ROOT/v2"
touch -t 202601030303 "$WEB_ROOT/v3"
touch -t 202602020202 "$WEB_ROOT/v5"
touch -t 202603030303 "$WEB_ROOT/v4"   # newest of all, and unprotected
prune_web_dirs "$WEB_ROOT/v5" >/dev/null
[[ -d $WEB_ROOT/v5 ]] && ok "keeps the live directory even when it is not the newest" \
  || bad "keeps the live directory even when it is not the newest" "v5 was pruned; the guard is comparing paths to basenames again"
[[ ! -e $WEB_ROOT/v1 ]] && ok "still removes directories beyond the retention count" || bad "still removes directories beyond the retention count"

make_bundle "$WEB_ROOT/rollbackme" "rb1"
touch -t 202601010101 "$WEB_ROOT/rollbackme"
prune_web_dirs "$WEB_ROOT/v5" "$WEB_ROOT/rollbackme" >/dev/null
[[ -d $WEB_ROOT/rollbackme ]] && ok "keeps the rollback directory even when it is the oldest" \
  || bad "keeps the rollback directory even when it is the oldest" "rollbackme was pruned"

# The live directory is now the OLDEST thing in the tree, and the `current` link
# -- which [[ -d ]] happily accepts -- sits alongside it. Both properties held at
# once in production: a bundle unpacked from a tarball carries the build
# machine's mtime, so it can post-date nothing, and publish_web_link always
# creates `current` fresh.
rm -rf "${WEB_ROOT:?}"/*; mkdir -p "$WEB_ROOT"
for n in v1 v2 v3 v4 v5; do make_bundle "$WEB_ROOT/$n" "$n"; done
touch -t 202601010101 "$WEB_ROOT/v5"     # live, and the oldest on disk
touch -t 202601020202 "$WEB_ROOT/v1"
touch -t 202601030303 "$WEB_ROOT/v2"
touch -t 202601040404 "$WEB_ROOT/v3"
touch -t 202603030303 "$WEB_ROOT/v4"     # newest, and unprotected
ln -sfn "$WEB_ROOT/v5" "$WEB_LINK"
prune_web_dirs "$WEB_ROOT/v5" >/dev/null
[[ -d $WEB_ROOT/v5 ]] && ok "keeps the live directory even when it is the oldest on disk" \
  || bad "keeps the live directory even when it is the oldest on disk" "v5 was pruned"
[[ -L $WEB_LINK ]] && ok "never prunes the current symlink" \
  || bad "never prunes the current symlink" "the link was removed"
kept=$(find "$WEB_ROOT" -mindepth 1 -maxdepth 1 -type d ! -name '.staging.*' | wc -l | tr -d ' ')
# v5 is protected and so is not charged for; the symlink is not a version at all
# and must not spend a slot either. Anything less and a real bundle is pruned a
# deploy early, which is the quiet version of this bug.
check "the current symlink does not spend a retention slot" "$kept" "$((WEB_KEEP + 1))"

echo "show_status"
mkdir -p "$BIN_DIR" "$APP_ROOT/data"
printf 'not-really-a-binary' > "$BIN_DIR/new-api"
status_out="$(show_status 2>&1)"
# supervisorctl answers "ERROR (no such process)" for the retired legacy
# program, which is the normal state -- printing it back makes a healthy host
# look like the one thing you reach for --status to rule out.
[[ $status_out == *"not registered with supervisord"* ]] \
  && ok "reports an unknown legacy program as absent" \
  || bad "reports an unknown legacy program as absent" "$status_out"
[[ $status_out != *ERROR* ]] && ok "never prints ERROR while the host is healthy" \
  || bad "never prints ERROR while the host is healthy" "$status_out"

echo
printf 'passed %d, failed %d\n' "$PASS" "$FAIL"
[[ $FAIL -eq 0 ]] || exit 1