#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_BIN="${GO:-}"
if [[ -z "${GO_BIN}" ]]; then
  GO_BIN="$(command -v go 2>/dev/null || true)"
fi
if [[ -z "${GO_BIN}" && -x "${ROOT}/.tools/go/bin/go" ]]; then
  GO_BIN="${ROOT}/.tools/go/bin/go"
fi
exec "${GO_BIN}" run "${ROOT}/test/acceptance/smoke"
