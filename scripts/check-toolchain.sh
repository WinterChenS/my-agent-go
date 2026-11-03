#!/usr/bin/env bash
# F00 toolchain gate (POSIX). Mirrors scripts/check-toolchain.ps1.
# Validates the *actually resolved* executables against build/versions.lock.json.
# Exit: 0 = satisfied, 2 = configuration missing / not satisfied.
set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCK_FILE="${1:-$SCRIPT_DIR/../build/versions.lock.json}"

errors=0

fail() { echo "FAIL: $1"; errors=$((errors+1)); }
pass() { echo "OK:   $1"; }

if [ ! -f "$LOCK_FILE" ]; then
  echo "FAIL: versions.lock.json not found at $LOCK_FILE (configuration missing)"
  exit 2
fi

# Extract expected versions from JSON (no jq dependency; tolerant grep).
exp_go=$(grep -o '"go"[[:space:]]*:[[:space:]]*"[^"]*"' "$LOCK_FILE" | head -1 | sed 's/.*:[[:space:]]*"\([^"]*\)"/\1/')
exp_node=$(grep -o '"node"[[:space:]]*:[[:space:]]*"[^"]*"' "$LOCK_FILE" | head -1 | sed 's/.*:[[:space:]]*"\([^"]*\)"/\1/')
exp_npm=$(grep -o '"npm"[[:space:]]*:[[:space:]]*"[^"]*"' "$LOCK_FILE" | head -1 | sed 's/.*:[[:space:]]*"\([^"]*\)"/\1/')
exp_wails=$(grep -o '"wails"[[:space:]]*:[[:space:]]*"[^"]*"' "$LOCK_FILE" | head -1 | sed 's/.*:[[:space:]]*"\([^"]*\)"/\1/')

[ -z "$exp_go" ] && { echo "FAIL: could not read 'go' from lock"; exit 2; }

echo "=== F00 toolchain check ($(date '+%Y-%m-%d %H:%M:%S')) ==="

# GO
go_path="$(command -v go || true)"
if [ -z "$go_path" ]; then fail "go: not found on PATH";
else
  go_ver="$(go version 2>&1)"
  echo "go: $go_path -> $go_ver"
  echo "$go_ver" | grep -q "go$exp_go" || fail "go: resolved to $go_path reports '$go_ver', expected go$exp_go"
fi

# NODE
node_path="$(command -v node || true)"
if [ -z "$node_path" ]; then fail "node: not found on PATH";
else
  node_ver="$(node -v 2>&1)"
  echo "node: $node_path -> $node_ver"
  [ "$node_ver" = "v$exp_node" ] || fail "node: resolved to '$node_path' (version '$node_ver'), expected v$exp_node"
fi

# NPM
npm_path="$(command -v npm || true)"
if [ -z "$npm_path" ]; then fail "npm: not found on PATH";
else
  npm_ver="$(npm -v 2>&1)"
  echo "npm: $npm_path -> $npm_ver"
  [ "$npm_ver" = "$exp_npm" ] || fail "npm: resolved to '$npm_path' (version '$npm_ver'), expected $exp_npm"
fi

# WAILS
wails_path="$(command -v wails3 || true)"
if [ -z "$wails_path" ]; then fail "wails3: not found on PATH";
else
  wails_ver="$(wails3 version 2>&1 || wails3 --version 2>&1)"
  echo "wails3: $wails_path -> $wails_ver"
  echo "$wails_ver" | grep -q "$exp_wails" || fail "wails3: resolved to '$wails_path' (version '$wails_ver'), expected $exp_wails"
fi

if [ "$errors" -eq 0 ]; then exit 0; else exit 2; fi
