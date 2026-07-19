#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LABEL="${1:-snapshot}"
ADMIN_URL="${PORTPILOT_DEV_ADMIN_URL:-http://127.0.0.1:5100}"
OUT_DIR="${ROOT}/.dev-data/logs/acceptance"

if [[ ! "${LABEL}" =~ ^[a-z0-9-]+$ ]]; then
  echo "Invalid snapshot label: ${LABEL}" >&2
  exit 1
fi

mkdir -p "${OUT_DIR}"
cookie="$(mktemp)"
trap 'rm -f "${cookie}"' EXIT

{
  date --iso-8601=seconds
  shopt -s nullglob
  pid_files=("${ROOT}"/.dev-data/dev-*.pids)
  for pid_file in "${pid_files[@]}"; do
    while IFS=":" read -r pid _ process_label; do
      if [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null; then
        printf "\n[%s pid=%s]\n" "${process_label}" "${pid}"
        ps --sid "${pid}" -o pid,ppid,pgid,stat,etime,cmd
      fi
    done <"${pid_file}"
  done
  ss -H -ltnup 2>/dev/null | grep -E ":(3000|5100|6100|7000|7001|7002|18080|18081)([[:space:]]|$)" || true
} >"${OUT_DIR}/${LABEL}-process.txt"

curl -fsS "${ADMIN_URL}/health/ready" >"${OUT_DIR}/${LABEL}-health.json"
curl -fsS -c "${cookie}" -H "Content-Type: application/json" \
  --data '{"user_name":"admin","password":"admin"}' \
  "${ADMIN_URL}/api/auth/login" >/dev/null
curl -fsS -b "${cookie}" "${ADMIN_URL}/api/metrics/summary" \
  >"${OUT_DIR}/${LABEL}-summary.json"
curl -fsS -b "${cookie}" "${ADMIN_URL}/api/clients" \
  >"${OUT_DIR}/${LABEL}-clients.json"
