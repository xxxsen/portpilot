#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
unset GOROOT
DATA_DIR="${PORTPILOT_DEV_DATA_DIR:-${ROOT}/.dev-data}"
LOG_DIR="${DATA_DIR}/logs"
CONFIG_DIR="${DATA_DIR}/config"
SERVER_CONFIG="${PORTPILOT_DEV_SERVER_CONFIG:-${CONFIG_DIR}/server.json}"
CLIENT_CONFIG="${PORTPILOT_DEV_CLIENT_CONFIG:-${CONFIG_DIR}/client.json}"
ADMIN_URL="${PORTPILOT_DEV_ADMIN_URL:-http://127.0.0.1:5100}"
WEB_URL="${PORTPILOT_DEV_WEB_URL:-http://127.0.0.1:3000}"
GO_BIN="${GO:-}"

if [[ -z "${GO_BIN}" ]]; then
  if [[ -x "${ROOT}/.tools/go/bin/go" ]]; then
    GO_BIN="${ROOT}/.tools/go/bin/go"
  elif command -v go >/dev/null 2>&1; then
    GO_BIN="$(command -v go)"
  else
    echo "Go is required. Install Go or place it at .tools/go/bin/go." >&2
    exit 1
  fi
fi
GO_TOOLCHAIN_VERSION="$("${GO_BIN}" env GOVERSION)"
export GOCACHE="${ROOT}/.cache/go-build/${GO_TOOLCHAIN_VERSION}"

mkdir -p "${LOG_DIR}" "${CONFIG_DIR}"
if [[ -z "${PORTPILOT_DEV_SERVER_CONFIG:-}" ]]; then
  jq '.data_db = "../portpilot.db"' "${ROOT}/configs/dev/server.json" >"${SERVER_CONFIG}.tmp"
  chmod 0600 "${SERVER_CONFIG}.tmp"
  mv "${SERVER_CONFIG}.tmp" "${SERVER_CONFIG}"
  install -m 0600 "${ROOT}/configs/dev/clients.json" "${CONFIG_DIR}/clients.json"
fi
if [[ -z "${PORTPILOT_DEV_CLIENT_CONFIG:-}" ]]; then
  install -m 0600 "${ROOT}/configs/dev/client.json" "${CLIENT_CONFIG}"
fi
repo_hash="$(printf '%s' "${ROOT}" | sha256sum | cut -c1-12)"
PID_FILE="${DATA_DIR}/dev-${repo_hash}.pids"
declare -a PIDS=()
declare -a LABELS=()
cleaned=0

process_start_time() {
  awk '{print $22}' "/proc/$1/stat" 2>/dev/null || true
}

cleanup_stale() {
  [[ -f "${PID_FILE}" ]] || return 0
  while IFS=':' read -r pid start label; do
    [[ "${pid}" =~ ^[0-9]+$ ]] || continue
    if [[ -d "/proc/${pid}" && "$(process_start_time "${pid}")" == "${start}" ]]; then
      echo "Stopping stale ${label} process (${pid})"
      kill -TERM -- "-${pid}" 2>/dev/null || kill -TERM "${pid}" 2>/dev/null || true
    fi
  done <"${PID_FILE}"
  sleep 0.3
  rm -f "${PID_FILE}"
}

# Invoked indirectly by the EXIT, INT, and TERM traps below.
# shellcheck disable=SC2329
cleanup() {
  [[ "${cleaned}" -eq 0 ]] || return 0
  cleaned=1
  trap - EXIT INT TERM
  for ((index = ${#PIDS[@]} - 1; index >= 0; index--)); do
    pid="${PIDS[index]}"
    if kill -0 "${pid}" 2>/dev/null; then
      kill -TERM -- "-${pid}" 2>/dev/null || kill -TERM "${pid}" 2>/dev/null || true
    fi
  done
  sleep 0.5
  for pid in "${PIDS[@]}"; do
    kill -KILL -- "-${pid}" 2>/dev/null || true
  done
  for pid in "${PIDS[@]}"; do
    wait "${pid}" 2>/dev/null || true
  done
  rm -f "${PID_FILE}"
}

start_process() {
  local label="$1"
  local log="$2"
  shift 2
  setsid "$@" >"${log}" 2>&1 &
  local pid=$!
  PIDS+=("${pid}")
  LABELS+=("${label}")
  printf '%s:%s:%s\n' "${pid}" "$(process_start_time "${pid}")" "${label}" >>"${PID_FILE}"
}

wait_http() {
  local name="$1"
  local url="$2"
  local attempts="${3:-100}"
  for ((i = 1; i <= attempts; i++)); do
    if curl -fsS "${url}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  echo "${name} did not become ready: ${url}" >&2
  return 1
}

wait_client_active() {
  local cookie="${DATA_DIR}/dev-cookie.txt"
  local login
  login="$(curl -fsS -c "${cookie}" -H 'Content-Type: application/json' \
    --data '{"user_name":"admin","password":"admin"}' "${ADMIN_URL}/api/auth/login")"
  if [[ "${login}" != *'"csrf_token"'* ]]; then
    echo "Admin login did not return a CSRF token" >&2
    return 1
  fi
  for ((i = 1; i <= 150; i++)); do
    local clients
    clients="$(curl -fsS -b "${cookie}" "${ADMIN_URL}/api/clients" 2>/dev/null || true)"
    if [[ "${clients}" == *'"client_id":"dev-client"'* && "${clients}" == *'"state":"ACTIVE"'* && "${clients}" =~ \"mux_sessions\":[1-9] ]]; then
      return 0
    fi
    sleep 0.1
  done
  echo "Client did not reach ACTIVE with a ready smux pool" >&2
  return 1
}

trap cleanup EXIT INT TERM
cleanup_stale
: >"${PID_FILE}"

DEV_BIN_DIR="${DATA_DIR}/bin"
mkdir -p "${DEV_BIN_DIR}"
"${GO_BIN}" build -o "${DEV_BIN_DIR}/portpilot-server" "${ROOT}/cmd/portpilot-server"
"${GO_BIN}" build -o "${DEV_BIN_DIR}/portpilot-client" "${ROOT}/cmd/portpilot-client"
if [[ "${PORTPILOT_DEV_ECHO:-1}" == "1" ]]; then
  "${GO_BIN}" build -o "${DEV_BIN_DIR}/echo" "${ROOT}/test/fixtures/echo"
  start_process "echo" "${LOG_DIR}/echo.log" "${DEV_BIN_DIR}/echo"
fi
start_process "server" "${LOG_DIR}/server.log" "${DEV_BIN_DIR}/portpilot-server" run --config "${SERVER_CONFIG}"
wait_http "PortPilot server" "${ADMIN_URL}/health/ready"

start_process "client" "${LOG_DIR}/client.log" "${DEV_BIN_DIR}/portpilot-client" run --config "${CLIENT_CONFIG}"
wait_client_active

start_process "web" "${LOG_DIR}/web.log" env PORTPILOT_ADMIN_API="${ADMIN_URL}" npm --prefix "${ROOT}/web" run dev
wait_http "PortPilot web" "${WEB_URL}"

cat <<EOF
PortPilot dev environment is ready
  Web:       ${WEB_URL}
  Admin API: ${ADMIN_URL}
  TCP proxy: 127.0.0.1:7000
  TCP direct: 127.0.0.1:7002
  TCP PROXY:  127.0.0.1:7003
  TCP limited: 127.0.0.1:7004
  UDP proxy: 127.0.0.1:7001
  Logs:      ${LOG_DIR}
  Smoke:     make smoke-dev
EOF

set +e
wait -n "${PIDS[@]}"
status=$?
set -e
if [[ "${status}" -eq 0 ]]; then
  echo "A dev process exited unexpectedly" >&2
else
  echo "A dev process failed with status ${status}" >&2
fi
exit 1
