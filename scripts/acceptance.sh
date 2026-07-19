#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG="${ROOT}/.dev-data/logs/acceptance-dev.log"
mkdir -p "$(dirname "${LOG}")"

cleanup() {
  local status=$?
  if [[ "${status}" -ne 0 && -n "${DEV_PID:-}" ]] && kill -0 "${DEV_PID}" 2>/dev/null; then
    "${ROOT}/scripts/snapshot-dev.sh" failure || true
  fi
  if [[ -n "${DEV_PID:-}" ]] && kill -0 "${DEV_PID}" 2>/dev/null; then
    kill -TERM -- "-${DEV_PID}" 2>/dev/null || kill -TERM "${DEV_PID}" 2>/dev/null || true
    wait "${DEV_PID}" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

setsid make -C "${ROOT}" dev >"${LOG}" 2>&1 &
DEV_PID=$!
ready=0
for ((i = 1; i <= 300; i++)); do
  if curl -fsS http://127.0.0.1:3000 >/dev/null 2>&1 && grep -q "PortPilot dev environment is ready" "${LOG}"; then
    ready=1
    break
  fi
  if ! kill -0 "${DEV_PID}" 2>/dev/null; then
    cat "${LOG}" >&2
    exit 1
  fi
  sleep 0.1
done
if [[ "${ready}" -ne 1 ]]; then
  echo "make dev did not become ready before the acceptance timeout" >&2
  cat "${LOG}" >&2
  exit 1
fi

"${ROOT}/scripts/snapshot-dev.sh" start
make -C "${ROOT}" smoke-dev
make -C "${ROOT}" web-e2e
make -C "${ROOT}" fault-test
make -C "${ROOT}" soak-test
"${ROOT}/scripts/snapshot-dev.sh" after-soak
make -C "${ROOT}" smoke-dev
"${ROOT}/scripts/snapshot-dev.sh" final
