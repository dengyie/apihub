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
environment=SESSION_SECRET="s3cr3t-value",SQLITE_PATH="$DEFAULT_APP_ROOT/data/new-api.db",APIHUB_STATIC_DIR="$WEB_ROOT/stale-dir"
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
grep -q "SQLITE_PATH=\"$DEFAULT_APP_ROOT/data/new-api.db\"\$" <<<"$ENVIRONMENT_LINE" \
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

APP_ROOT="$DEFAULT_APP_ROOT"; SUPERVISOR_CONF_DIR_EXPLICIT=""
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

echo "do_rollback"

# do_rollback is the one path that stops BOTH slots at once, so it is the one
# place where "validate everything, then act" has to hold rather than being
# merely advisable. A refusal after the stop loop costs the site; the same
# refusal before it costs a message. Nothing here touches supervisor, the port
# or a real deployment -- supervisor and the readiness probe are stubbed, so the
# assertions are about call ORDER and about what ends up in the slot configs.
RB_CALLS="$WORK/rollback.calls"
# Captured BEFORE the stubs below, so this is the real definition and not the
# `:` no-op that replaces it two lines down.
RB_REAL_APPLY="$(declare -f apply_supervisor_conf)"
sup() { printf 'sup %s\n' "$*" >>"$RB_CALLS"; return 0; }
need_root() { :; }
apply_supervisor_conf() { :; }
wait_ready() { return 0; }
verify_frontend_served() { return 0; }
slot_pid() { echo 4242; }

# A backup as do_backup leaves it, plus one artifact that can answer --version.
rb_setup() {
  rm -rf "$WORK/rb"; mkdir -p "$WORK/rb/backup" "$SUPERVISOR_CONF_DIR" "$BIN_DIR"
  # source_environment reads the legacy config first, and earlier cases in this
  # file leave one behind. Remove it so the environment line under test is the
  # one rb_setup wrote rather than whichever fixture happened to run last.
  rm -f "$SUPERVISOR_CONF_DIR/$LEGACY_PROGRAM.conf"
  cat >"$WORK/rb/backup/new-api.prev" <<'PREV'
#!/usr/bin/env bash
[[ ${1:-} == --version ]] && { printf 'v29.36+0c6da1c8fdbaf4a1f31bf98c1c7ad04a740fde2d\n'; exit 0; }
exit 1
PREV
  chmod +x "$WORK/rb/backup/new-api.prev"
  # What deploy.sh writes: the version this deploy was moving TO. This is the
  # string that must never end up labelling a restored binary.
  printf 'v29.36+721b25c3cd81ded8422186e11552fd855e8caac9' \
    >"$WORK/rb/backup/deploy-state.version"
  printf 'apihub-green' >"$WORK/rb/backup/deploy-state.slot"
  printf 'binary-being-replaced\n' >"$BIN_DIR/new-api"
  printf '%s' "$WORK/rb/backup" >"$WORK/rb/state"
  STATE_FILE="$WORK/rb/state"
  cat >"$SUPERVISOR_CONF_DIR/apihub-green.conf" <<'CONF'
[program:apihub-green]
environment=SESSION_SECRET="s3cr3t-value",PORT=3000
CONF
  : >"$RB_CALLS"
}

# 1. Order. A backup with no binary in it must be refused before anything is
#    stopped. Under the old order both slots went down first and the refusal
#    arrived afterwards, which is the 2026-10-09 outage shape exactly.
rb_setup
rm -f "$WORK/rb/backup/new-api.prev"
( do_rollback ) >/dev/null 2>&1
rc=$?
check "refuses a backup with no binary" "$rc" "1"
if grep -q 'sup stop' "$RB_CALLS"; then
  bad "refuses BEFORE stopping any slot" "$(tr '\n' ';' <"$RB_CALLS")"
else
  ok "refuses BEFORE stopping any slot"
fi

# 2. Order, again, for the metadata the old code required. It is now derived
#    from the binary, so a backup without it is still usable -- which is what
#    makes the bootstrap path (which never wrote it) recoverable.
rb_setup
rm -f "$WORK/rb/backup/deploy-state.version"
out="$( ( do_rollback ) 2>&1 )"
rc=$?
check "a backup without deploy-state.version is still usable" "$rc" "0"
if grep -q 'sup stop' "$RB_CALLS"; then
  ok "and it reached the point of stopping the slots"
else
  bad "and it reached the point of stopping the slots" "$out"
fi

# 3. The version a rollback stamps comes from the binary it restored, not from
#    the file deploy.sh wrote about the version it was rolling back FROM.
#    VERSION is an environment override, so trusting that file labelled the old
#    code with the new name -- and made wait_ready's version check compare a
#    string with itself, on the one path where a wrong binary is likeliest.
rb_setup
( do_rollback ) >/dev/null 2>&1
green_env="$(env_line_of apihub-green)"
if grep -q 'VERSION="v29.36+0c6da1c8fdbaf4a1f31bf98c1c7ad04a740fde2d"' <<<"$green_env"; then
  ok "stamps the restored slot with the version the binary reports"
else
  bad "stamps the restored slot with the version the binary reports" "$green_env"
fi
if grep -q 'VERSION="v29.36+721b25c3cd81ded8422186e11552fd855e8caac9"' <<<"$green_env"; then
  bad "never stamps the restored slot with the version it rolled back FROM" "$green_env"
else
  ok "never stamps the restored slot with the version it rolled back FROM"
fi
head -1 "$BIN_DIR/new-api" | grep -q '^#!' \
  && ok "the previous binary is what ends up installed" \
  || bad "the previous binary is what ends up installed" "$(head -1 "$BIN_DIR/new-api")"

# 4. Rollback is one-way unless the outgoing binary is kept. It exists nowhere
#    else afterwards -- CI deletes the upload -- so without this a second
#    rollback has nothing to roll back to.
if grep -q 'binary-being-replaced' "$WORK/rb/backup/new-api.rolled-back-from" 2>/dev/null; then
  ok "keeps the binary it rolled back FROM"
else
  bad "keeps the binary it rolled back FROM" "$(ls -1 "$WORK/rb/backup")"
fi

echo "apply_supervisor_conf"

# A failed reload and "the config was already correct" must not look alike. The
# only other check that would notice is the frontend fingerprint, which does not
# run until after the serving slot has been retired.
eval "$RB_REAL_APPLY"
# A real executable on PATH, so need_cmd still has to pass for the right reason
# -- this machine has no supervisorctl of its own.
mkdir -p "$WORK/fakebin"
printf '#!/bin/sh\nexit 0\n' >"$WORK/fakebin/supervisorctl"
chmod +x "$WORK/fakebin/supervisorctl"
PATH="$WORK/fakebin:$PATH"

UPDATE_OUT="apihub-green: updated process group"
UPDATE_RC=0
sup() {
  case "${1:-}" in
    status) echo "apihub-green RUNNING pid 1, uptime 0:01:00"; return 0 ;;
    update) [[ -n $UPDATE_OUT ]] && printf '%s\n' "$UPDATE_OUT"; return "$UPDATE_RC" ;;
    *)      return 0 ;;
  esac
}
UPDATE_RC=1
out="$( ( apply_supervisor_conf ) 2>&1 )"; rc=$?
check "reports a failed supervisorctl update" "$rc" "1"
[[ $out == *updated* ]] \
  && ok "and repeats what update said" \
  || bad "and repeats what update said" "$out"
UPDATE_RC=0
out="$( ( apply_supervisor_conf ) 2>&1 )"; rc=$?
check "a successful update is not an error" "$rc" "0"

echo "install_slot_guard"

# The guard is installed and registered by deploy.sh, not armed by hand. A
# hand-armed guard was correct code with no lifecycle: gone after the next
# reboot, while the runbook described it as a standing protection.
GUARD_PATH="$APP_ROOT/slot-watchdog.sh"
GUARD_CONF="$SUPERVISOR_CONF_DIR/apihub-slot-guard.conf"
WATCHDOG_SRC="$SCRIPT_DIR/slot-watchdog.sh"
rm -f "$GUARD_PATH" "$GUARD_CONF"
install_slot_guard >/dev/null 2>&1
[[ -x $GUARD_PATH ]] && ok "installs the guard as an executable under the deployment root" \
  || bad "installs the guard as an executable under the deployment root" "$GUARD_PATH"
grep -q "command=$GUARD_PATH " "$GUARD_CONF" \
  && ok "the supervisor program runs the installed copy, not the upload" \
  || bad "the supervisor program runs the installed copy, not the upload" "$(grep '^command=' "$GUARD_CONF")"
cmp -s "$GUARD_PATH" "$WATCHDOG_SRC" \
  && ok "the installed guard matches the reviewed source" \
  || bad "the installed guard matches the reviewed source" "differs"

# supervisorctl update only restarts a program whose CONFIG changed, so a fixed
# config would keep running old code forever. The digest is what ties the two
# together; without it, editing the guard would silently do nothing.
guard_conf_before="$(cat "$GUARD_CONF")"
cp -a "$WATCHDOG_SRC" "$WORK/watchdog.src.bak"
printf '\n# touched\n' >>"$WATCHDOG_SRC"
install_slot_guard >/dev/null 2>&1
[[ $(cat "$GUARD_CONF") != "$guard_conf_before" ]] \
  && ok "a changed guard produces a changed config, so update restarts it" \
  || bad "a changed guard produces a changed config, so update restarts it" "identical"
cp -a "$WORK/watchdog.src.bak" "$WATCHDOG_SRC"

# A manual run from an old checkout has no guard to upload. That must skip, not
# fail: the guard is a protection, not a precondition for deploying.
WATCHDOG_SRC="$WORK/not-here.sh"
guard_before="$(cat "$GUARD_PATH")"
out="$( ( install_slot_guard ) 2>&1 )"; rc=$?
check "a missing guard source is not a deploy failure" "$rc" "0"
[[ $(cat "$GUARD_PATH") == "$guard_before" ]] \
  && ok "and leaves the installed guard alone" \
  || bad "and leaves the installed guard alone" "the installed guard was replaced"
[[ $out == *no\ slot\ guard* ]] \
  && ok "and says why it skipped" \
  || bad "and says why it skipped" "$out"
rm -f "$WATCHDOG_SRC.bak"

echo "prune_backup_dirs"
# do_backup writes a full copy of the binary and a database snapshot into a fresh
# timestamped directory on every deploy. Nothing removed the old ones, so backups/
# grew by ~350MB per deployment with no ceiling -- 7.7GB after a day and a half,
# the largest consumer on a 98G root filesystem. These assertions pin the two
# guards that make deleting them safe.
BD="$WORK/backups"
mk_backup() { mkdir -p "$BD/$1"; printf 'x' >"$BD/$1/new-api.prev"; printf 'x' >"$BD/$1/new-api.db"; }
BACKUP_DIR="$BD"

rm -rf "$BD"; mkdir -p "$BD"
for n in 20261001-010101 20261002-010101 20261003-010101 20261004-010101 \
         20261005-010101 20261006-010101 20261007-010101; do mk_backup "$n"; done
# Hand-named safety copies. These are not deploy residue and must never be a
# candidate, however old they get.
for n in stale-db-snapshots autorecover-20261002-034614 \
         pre-del-401-20261002-0410 whitelist-20261002-015012; do mk_backup "$n"; done

prune_backup_dirs "$BD/20261007-010101" >/dev/null
[[ ! -e $BD/20261001-010101 ]] \
  && ok "removes deploy backups past the retention count" \
  || bad "removes deploy backups past the retention count" "the oldest deploy backup is still there"
kept=$(find "$BD" -mindepth 1 -maxdepth 1 -type d -name '2026*' | wc -l | tr -d ' ')
check "keeps BACKUP_KEEP deploy backups plus the protected one" "$kept" "$((BACKUP_KEEP + 1))"
for n in stale-db-snapshots autorecover-20261002-034614 \
         pre-del-401-20261002-0410 whitelist-20261002-015012; do
  [[ -d $BD/$n ]] || bad "never touches hand-named safety copies" "$n was pruned"
done
ok "never touches hand-named safety copies"

# The count above passes for the wrong reason if the protected entry is also the
# newest: the retention counter would have kept it anyway. Pin the order so the
# guard is the only thing that can save it -- the rollback target is whatever the
# last deploy recorded, which is old exactly when a rollback has moved us back.
rm -rf "$BD"; mkdir -p "$BD"
BACKUP_KEEP=1
for n in 20261001-010101 20261005-010101 20261007-010101; do mk_backup "$n"; done
mk_backup stale-db-snapshots
# A directory whose name only looks like a stamp must not be swept up either.
mk_backup 20261008
mk_backup 20261008-0101
ln -sfn "$BD/20261007-010101" "$BD/20261009-010101"
prune_backup_dirs "$BD/20261001-010101" >/dev/null
[[ -d $BD/20261001-010101 ]] \
  && ok "keeps the rollback target even when it is the oldest backup" \
  || bad "keeps the rollback target even when it is the oldest backup" "pruned; the guard is not protecting it"
[[ ! -e $BD/20261005-010101 ]] \
  && ok "still prunes past the retention count with a protected entry present" \
  || bad "still prunes past the retention count with a protected entry present" "nothing was pruned"
[[ -d $BD/stale-db-snapshots && -d $BD/20261008 && -d $BD/20261008-0101 ]] \
  && ok "leaves near-miss names and hand-named copies alone" \
  || bad "leaves near-miss names and hand-named copies alone" "a name that is not YYYYMMDD-HHMMSS was pruned"
[[ -L $BD/20261009-010101 ]] \
  && ok "never deletes a symlink that looks like a backup" \
  || bad "never deletes a symlink that looks like a backup" "the link was removed"
[[ -d $BD/20261007-010101 ]] \
  && ok "and does not follow it to the real directory" \
  || bad "and does not follow it to the real directory" "rm -rf followed the link"

# Fewer backups than the budget: pruning must be a no-op, not an excuse to delete.
rm -rf "$BD"; mkdir -p "$BD"
BACKUP_KEEP=5
mk_backup 20261007-010101
mk_backup 20261006-010101
prune_backup_dirs >/dev/null
check "leaves everything alone below the retention count" \
  "$(find "$BD" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')" "2"

# A missing backup directory is a fresh install, not a reason to fail a deploy.
BACKUP_DIR="$WORK/not-created-yet"
prune_backup_dirs >/dev/null 2>&1 && ok "a missing backup directory is not an error" \
  || bad "a missing backup directory is not an error"
BACKUP_DIR="$BD"

# ---------------------------------------------------------------------------
# BACKUP_KEEP is read into (( )) arithmetic inside prune_backup_dirs, and bash
# resolves a bare non-numeric operand there as an unset VARIABLE NAME, which is
# 0. "5x" therefore printed an arithmetic complaint to stderr and then compared
# as "1 <= 0": false, so it deleted every deploy backup except the rollback
# target. The complaint is not a failure -- the function kept going and pruned.
# The validation lives at the top level of deploy.sh (it has to, since the
# variable is consumed long after any argument parsing), so it is exercised in
# a subprocess rather than by re-sourcing.
# ---------------------------------------------------------------------------
echo "BACKUP_KEEP validation"

run_with_backup_keep() {
  BACKUP_KEEP="$1" bash "$SCRIPT_DIR/deploy.sh" --status 2>&1 | head -1
}

for badv in 5x abc -1 3.5; do
  out="$(run_with_backup_keep "$badv")"
  if [[ $out == *"BACKUP_KEEP must be a non-negative integer"* ]]; then
    ok "rejects a malformed BACKUP_KEEP ($badv) instead of pruning everything"
  else
    bad "rejects a malformed BACKUP_KEEP ($badv) instead of pruning everything" "$out"
  fi
done

# The guard must not fire on the values an operator actually uses, or it would
# break a working deploy to defend against a typo. Getting past it is visible as
# the script reaching the next check instead of complaining about BACKUP_KEEP.
for goodv in 0 1 5 25; do
  out="$(run_with_backup_keep "$goodv")"
  if [[ $out != *"BACKUP_KEEP must be"* ]]; then
    ok "accepts a valid BACKUP_KEEP ($goodv)"
  else
    bad "accepts a valid BACKUP_KEEP ($goodv)" "$out"
  fi
done

# ---------------------------------------------------------------------------
# check-deploy-refs.sh once excluded scripts/deploy.sh from the /tmp/mnt scan,
# to let a single explanatory mention in a comment through. But scan() already
# drops comment lines, so the exclusion bought nothing -- and it blinded the
# check on the one file every path on the host is derived from. Injecting
# DEFAULT_APP_ROOT="/tmp/mnt/new-api" passed the check while deploy.sh was
# excluded. Asserting the absence of that exclusion is what keeps the hole shut.
# ---------------------------------------------------------------------------
echo "deployment layout check coverage"

if grep -q "':!scripts/deploy.sh'" "$SCRIPT_DIR/check-deploy-refs.sh"; then
  bad "the layout check still scans deploy.sh" \
      "scripts/check-deploy-refs.sh excludes scripts/deploy.sh from the retired-root scan"
else
  ok "the layout check still scans deploy.sh"
fi

if bash "$SCRIPT_DIR/check-deploy-refs.sh" >/dev/null 2>&1; then
  ok "the layout check passes on a clean tree"
else
  bad "the layout check passes on a clean tree" "it reported a violation with no changes staged"
fi

# ---------------------------------------------------------------------------
# The production gate. `needs:` cannot cross a workflow boundary, so deploy.yml
# has to call ci.yml as a reusable workflow -- and the two facts that make that
# gate real (the call, and the deploy job actually waiting on it) are both easy
# to delete without anything failing. A deploy that runs alongside a red CI is
# indistinguishable from a correct one until you read the YAML.
# ---------------------------------------------------------------------------
echo "production release gate"

deploy_wf="$SCRIPT_DIR/../.github/workflows/deploy.yml"
ci_wf="$SCRIPT_DIR/../.github/workflows/ci.yml"

if [ ! -f "$deploy_wf" ] || [ ! -f "$ci_wf" ]; then
  bad "the release gate is wired up" "a workflow file is missing from $deploy_wf"
elif grep -q 'uses: \./\.github/workflows/ci\.yml' "$deploy_wf"; then
  ok "deploy.yml calls the CI workflow as a gate"
else
  bad "deploy.yml calls the CI workflow as a gate" \
      "no 'uses: ./.github/workflows/ci.yml' job in deploy.yml; CI and deploy race"
fi

if grep -q 'workflow_call' "$ci_wf"; then
  ok "ci.yml accepts being called"
else
  bad "ci.yml accepts being called" "ci.yml has no workflow_call trigger, so the gate cannot run"
fi

if grep -qE '^[[:space:]]+needs:[[:space:]]*verify[[:space:]]*$' "$deploy_wf"; then
  ok "the deploy job waits for the gate"
else
  bad "the deploy job waits for the gate" \
      "no job in deploy.yml declares 'needs: verify'; the build/deploy are ungated"
fi

if grep -qE '^[[:space:]]+environment:[[:space:]]*production[[:space:]]*$' "$deploy_wf"; then
  ok "production is still gated by its environment"
else
  bad "production is still gated by its environment" "no environment: production in deploy.yml"
fi

# The gate is worthless if the deploy job rebuilds and ships something the gate
# never inspected, so the build must be a separate job handing over an artifact.
if grep -q 'needs: build' "$deploy_wf" && grep -q 'download-artifact@' "$deploy_wf"; then
  ok "the deploy ships the artifact the build job verified"
else
  bad "the deploy ships the artifact the build job verified" \
      "deploy.yml does not download a build artifact; it builds its own binary"
fi

echo
printf 'passed %d, failed %d\n' "$PASS" "$FAIL"
[[ $FAIL -eq 0 ]] || exit 1