#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DEVUP_BIN="$REPO_ROOT/devup"
APP_MANIFEST="$REPO_ROOT/examples/react-python-demo/devup.app.yaml"
CHECK_MOUNT="$REPO_ROOT:/workspace"
CHECK_WORKDIR="/workspace"

cleanup() {
  "$DEVUP_BIN" app down --file "$APP_MANIFEST" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> Build devup"
cd "$REPO_ROOT"
go build -o "$DEVUP_BIN" ./cmd/devup

echo "==> Start demo stack"
"$DEVUP_BIN" app down --file "$APP_MANIFEST" >/dev/null 2>&1 || true
"$DEVUP_BIN" app up --file "$APP_MANIFEST"

echo "==> Wait for backend health"
for _ in $(seq 1 20); do
  if "$DEVUP_BIN" run --mount "$CHECK_MOUNT" --workdir "$CHECK_WORKDIR" -- bash -lc 'curl -fsS http://127.0.0.1:8000/api/health' | grep -q '"status": "ok"'; then
    break
  fi
  sleep 1
done
"$DEVUP_BIN" run --mount "$CHECK_MOUNT" --workdir "$CHECK_WORKDIR" -- bash -lc 'curl -fsS http://127.0.0.1:8000/api/health' | grep -q '"service": "backend"' || {
  echo "FAIL: backend health endpoint not reachable"
  exit 1
}

echo "==> Wait for frontend dev server"
for _ in $(seq 1 40); do
  if "$DEVUP_BIN" run --mount "$CHECK_MOUNT" --workdir "$CHECK_WORKDIR" -- bash -lc 'curl -fsS http://127.0.0.1:3000/' | grep -q 'Devup React + Python Demo'; then
    break
  fi
  sleep 1
done
"$DEVUP_BIN" run --mount "$CHECK_MOUNT" --workdir "$CHECK_WORKDIR" -- bash -lc 'curl -fsS http://127.0.0.1:3000/' | grep -q 'Devup React + Python Demo' || {
  echo "FAIL: frontend root page not reachable"
  exit 1
}

echo "==> Verify frontend proxy reaches backend"
"$DEVUP_BIN" run --mount "$CHECK_MOUNT" --workdir "$CHECK_WORKDIR" -- bash -lc 'curl -fsS http://127.0.0.1:3000/api/message' | grep -q 'Hello from the Python backend' || {
  echo "FAIL: frontend proxy did not reach backend"
  exit 1
}

echo "==> Check app state"
ps_out=$("$DEVUP_BIN" app ps --file "$APP_MANIFEST")
echo "$ps_out" | grep -q 'frontend' || { echo "FAIL: frontend missing from app ps"; exit 1; }
echo "$ps_out" | grep -q 'backend' || { echo "FAIL: backend missing from app ps"; exit 1; }
echo "$ps_out" | grep -q 'running' || { echo "FAIL: app ps should show running services"; exit 1; }

echo "PASS"
