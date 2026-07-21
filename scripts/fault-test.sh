#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
unset GOROOT
GO_BIN="${GO:-}"
if [[ -z "${GO_BIN}" && -x "${ROOT}/.tools/go/bin/go" ]]; then
  GO_BIN="${ROOT}/.tools/go/bin/go"
fi
if [[ -z "${GO_BIN}" ]]; then
  GO_BIN="$(command -v go 2>/dev/null || true)"
fi
if [[ -z "${GO_BIN}" ]]; then
  echo "Go is required. Install Go or place it at .tools/go/bin/go." >&2
  exit 1
fi
GO_TOOLCHAIN_VERSION="$("${GO_BIN}" env GOVERSION)"
export GOCACHE="${ROOT}/.cache/go-build/${GO_TOOLCHAIN_VERSION}"

"${GO_BIN}" test \
  "${ROOT}/internal/auth" \
  "${ROOT}/internal/protocol" \
  "${ROOT}/internal/server" \
  "${ROOT}/internal/config" \
  "${ROOT}/internal/relay" \
  "${ROOT}/internal/store"
PORTPILOT_DEV_SERVER_LOG="${PORTPILOT_DEV_SERVER_LOG:-${ROOT}/.dev-data/logs/server.log}" \
  exec "${GO_BIN}" run "${ROOT}/test/acceptance/fault"
