#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_BIN="${GO:-$(command -v go 2>/dev/null || true)}"
if [[ -z "${GO_BIN}" ]]; then
  GO_BIN="${ROOT}/.tools/go/bin/go"
fi

"${GO_BIN}" test \
  "${ROOT}/internal/auth" \
  "${ROOT}/internal/protocol" \
  "${ROOT}/internal/server" \
  "${ROOT}/internal/config" \
  "${ROOT}/internal/relay" \
  "${ROOT}/internal/store"
exec "${GO_BIN}" run "${ROOT}/test/acceptance/fault"
