#!/usr/bin/env bash
# web-local-smoke.sh — boot the freshly built aero-vault binary in a hermetic
# temp environment and smoke the embedded web UI (shell + runtime config +
# assets) through headless Chrome.
#
# Fail-closed by design (see .git/hooks/pre-push): a missing binary, a missing
# Chrome, a server that will not become ready, a non-2xx asset, a console error
# or an uncaught exception all abort the run. `git push --no-verify` is the only
# escape hatch and the hook header reserves it for documented emergencies.
#
# Layered checks:
#   1. static  — /ui/, /ui/app/, /ui/legacy/, /favicon.ico and every asset the
#                embedded index.html references must answer 2xx over HTTP;
#   2. runtime — headless Chrome loads the shell, must see zero console errors,
#                zero failed requests and a mounted React #root with the
#                window.__AERO_VAULT_WEB_CONFIG__ runtime config applied.
#
# Usage: scripts/web-local-smoke.sh [path-to-binary]
# Env:   AERO_SMOKE_BIN       override the binary under test
#        AERO_SMOKE_CHROME    override browser discovery
#        AERO_SMOKE_TIMEOUT_SEC  per-page browser budget (default 25)
#        AERO_SMOKE_KEEP=1    keep the temp workdir and print its path
set -euo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 1

BIN="${1:-${AERO_SMOKE_BIN:-bin/aero-vault}}"
TIMEOUT_SEC="${AERO_SMOKE_TIMEOUT_SEC:-25}"
READY_TIMEOUT_SEC="${AERO_SMOKE_READY_TIMEOUT_SEC:-45}"

die() { echo "❌ $*" >&2; exit 1; }
step() { echo "  $*"; }

# ── 1. Toolchain ────────────────────────────────────────────────────────────
command -v node >/dev/null 2>&1 || die "node is required (Node >= 22 for the built-in WebSocket client)"
NODE_MAJOR="$(node -p 'process.versions.node.split(".")[0]')"
[ "$NODE_MAJOR" -ge 22 ] || die "node >= 22 required for the built-in WebSocket client, found $(node -v)"

find_chrome() {
    local candidate
    for candidate in "${AERO_SMOKE_CHROME:-}" google-chrome-stable google-chrome chromium chromium-browser \
        /Applications/Google\ Chrome.app/Contents/MacOS/Google\ Chrome; do
        [ -n "$candidate" ] || continue
        if command -v "$candidate" >/dev/null 2>&1; then command -v "$candidate"; return 0; fi
        [ -x "$candidate" ] && { echo "$candidate"; return 0; }
    done
    # Last resort: a puppeteer-managed download.
    local cache="${PUPPETEER_CACHE_DIR:-$HOME/.cache/puppeteer}"
    [ -d "$cache" ] || return 1
    find "$cache" -maxdepth 4 -type f -name 'chrome' -perm -u+x 2>/dev/null | head -n 1
}

CHROME="$(find_chrome || true)"
[ -n "$CHROME" ] || die "no Chrome/Chromium found (set AERO_SMOKE_CHROME, or install google-chrome / chromium)"

[ -f "$BIN" ] || die "binary not found: $BIN (run 'make build' first)"
[ -x "$BIN" ] || die "binary is not executable: $BIN (run 'make build' first)"
command -v curl >/dev/null 2>&1 || die "curl is required"

echo "==> web smoke: binary=$BIN browser=$CHROME node=$(node -v)"

# ── 2. Hermetic server boot ─────────────────────────────────────────────────
WORK="$(mktemp -d "${TMPDIR:-/tmp}/aero-web-smoke-XXXXXX")"
PORT="$(node -e 'const s=require("net").createServer();s.listen(0,"127.0.0.1",()=>{const p=s.address().port;s.close(()=>console.log(p))})')"
[ -n "$PORT" ] || die "could not reserve a local port"
BASE="http://127.0.0.1:$PORT"
SERVER_PID=""

cleanup() {
    local code=$?
    if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
        for _ in 1 2 3 4 5 6 7 8 9 10; do
            kill -0 "$SERVER_PID" 2>/dev/null || break
            sleep 0.2
        done
        kill -9 "$SERVER_PID" 2>/dev/null || true
    fi
    if [ "${AERO_SMOKE_KEEP:-0}" = "1" ]; then
        echo "==> workdir kept: $WORK"
    else
        rm -rf "$WORK"
    fi
    return $code
}
trap cleanup EXIT

# `env -i` + a cwd without .env keeps the run hermetic: repo-local .env (auth,
# S3, AI, object lock …) can never leak into the gate, and a stray local config
# can never turn a UI regression into a false pass.
step "booting server on $BASE (sqlite + local fs, webui on, no auth)"
(
    cd "$WORK" &&
    env -i \
        PATH="$PATH" \
        HOME="$HOME" \
        APP_ADDR="127.0.0.1:$PORT" \
        APP_LOG_LEVEL="${AERO_SMOKE_LOG_LEVEL:-warn}" \
        STORAGE_BACKEND=local \
        STORAGE_LOCAL_ROOT="$WORK/objects" \
        STORAGE_LOCAL_SIGN_KEY="web-smoke-sign-key" \
        DB_DRIVER=sqlite \
        DB_DSN="file:$WORK/aero.db?_pragma=foreign_keys(1)" \
        WEBUI_ENABLED=true \
        S3_COMPAT_PREFIX= \
        "$OLDPWD/$BIN"
) >"$WORK/server.log" 2>&1 &
SERVER_PID=$!

ready=0
deadline=$((SECONDS + READY_TIMEOUT_SEC))
while [ "$SECONDS" -lt "$deadline" ]; do
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then break; fi
    if curl -fsS --max-time 2 "$BASE/readyz" >/dev/null 2>&1; then ready=1; break; fi
    sleep 0.3
done
if [ "$ready" -ne 1 ]; then
    echo "--- server log ---" >&2
    tail -n 40 "$WORK/server.log" >&2 || true
    die "server did not become ready on $BASE within ${READY_TIMEOUT_SEC}s"
fi
step "ready: $(curl -fsS --max-time 2 "$BASE/readyz")"

# ── 3. Static shell + asset checks ──────────────────────────────────────────
http_status() { curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$BASE$1"; }

expect_status() {
    local path="$1" want="$2" got
    got="$(http_status "$path")"
    [ "$got" = "$want" ] || die "GET $path returned HTTP $got (want $want)"
    step "GET $path → $got"
}

expect_status "/" 302
expect_status "/ui/" 200
expect_status "/ui/app/" 200
expect_status "/ui/legacy/" 200
expect_status "/favicon.ico" 200
expect_status "/ui/app/runtime-config.js" 200

curl -fsS --max-time 5 "$BASE/ui/app/runtime-config.js" | grep -q '__AERO_VAULT_WEB_CONFIG__' \
    || die "/ui/app/runtime-config.js does not define window.__AERO_VAULT_WEB_CONFIG__"

SHELL_HTML="$(curl -fsS --max-time 5 "$BASE/ui/")"
grep -q '<div id="root">' <<<"$SHELL_HTML" || die "/ui/ shell has no #root mount point"
grep -q '/ui/app/runtime-config.js' <<<"$SHELL_HTML" || die "/ui/ shell does not load the runtime config"
step "shell: #root mount point + runtime-config.js present"

# Every asset the embedded shell references must resolve — a stale index.html
# asset hash (built app not rebuilt into the embed) fails here.
assets=0
while read -r ref; do
    [ -n "$ref" ] || continue
    expect_status "$ref" 200
    assets=$((assets + 1))
done < <(grep -oE '(src|href)="/ui/app/assets/[^"]+"' <<<"$SHELL_HTML" | sed -E 's/^(src|href)="//; s/"$//' | sort -u)
[ "$assets" -gt 0 ] || die "/ui/ shell references no /ui/app/assets/ bundle"
step "assets: $assets bundle/style reference(s) resolved"

# ── 4. Headless browser runtime smoke ───────────────────────────────────────
step "driving headless Chrome over CDP (paths: /ui/ /ui/app/)"
if ! node scripts/web-smoke.mjs \
        --base-url "$BASE" \
        --chrome "$CHROME" \
        --path /ui/ --path /ui/app/ \
        --timeout "$((TIMEOUT_SEC * 1000))"; then
    echo "--- server log (tail) ---" >&2
    tail -n 20 "$WORK/server.log" >&2 || true
    die "browser smoke failed"
fi

echo "==> ✅ UI browser smoke passed ($BASE, $assets asset(s))"
