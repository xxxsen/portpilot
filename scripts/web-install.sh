#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="${ROOT}/web"
CACHE_DIR="${ROOT}/.cache"
HASH_FILE="${CACHE_DIR}/web-package-lock.sha256"

mkdir -p "${CACHE_DIR}"
current_hash="$(sha256sum "${WEB_DIR}/package-lock.json" | awk '{print $1}')"
if [[ -d "${WEB_DIR}/node_modules" && -f "${HASH_FILE}" && "$(cat "${HASH_FILE}")" == "${current_hash}" ]]; then
  exit 0
fi

(cd "${WEB_DIR}" && npm ci --no-audit --no-fund)
printf '%s\n' "${current_hash}" >"${HASH_FILE}"
