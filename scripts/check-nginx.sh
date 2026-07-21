#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG="${ROOT}/docker/nginx/default.conf"

# shellcheck disable=SC2016
required=(
  'ssl_protocols TLSv1.2 TLSv1.3;'
  'location = /portpilot/control'
  'location ~ ^/portpilot/data/(mux|direct)$'
  'proxy_pass http://127.0.0.1:6100;'
  'proxy_set_header X-Real-IP $remote_addr;'
  'proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;'
  'proxy_set_header X-Forwarded-Proto https;'
  'proxy_request_buffering off;'
  'proxy_buffering off;'
)

for pattern in "${required[@]}"; do
  grep -Fq "${pattern}" "${CONFIG}" || {
    echo "Nginx sample is missing required directive: ${pattern}" >&2
    exit 1
  }
done

if grep -Eq 'TLSv1(\.0|\.1)?([ ;]|$)|proxy_ssl_verify[[:space:]]+off' "${CONFIG}"; then
  echo "Nginx sample enables a forbidden TLS setting" >&2
  exit 1
fi
