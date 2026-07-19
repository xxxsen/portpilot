#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_BIN="${GO:-$(command -v go 2>/dev/null || true)}"
if [[ -z "${GO_BIN}" ]]; then GO_BIN="${ROOT}/.tools/go/bin/go"; fi
exec "${GO_BIN}" run "${ROOT}/test/acceptance/soak"
