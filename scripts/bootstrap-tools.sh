#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CACHE="${ROOT}/.cache/tools"
BIN="${ROOT}/bin"
SHELLCHECK_VERSION="0.11.0"
HADOLINT_VERSION="2.14.0"

mkdir -p "${CACHE}" "${BIN}"

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64)
    shellcheck_arch="x86_64"
    shellcheck_sha="b7af85e41cc99489dcc21d66c6d5f3685138f06d34651e6d34b42ec6d54fe6f6"
    hadolint_arch="x86_64"
    hadolint_sha="6bf226944684f56c84dd014e8b979d27425c0148f61b3bd99bcc6f39e9dc5a47"
    ;;
  Linux/aarch64)
    shellcheck_arch="aarch64"
    shellcheck_sha="68a8133197a50beb8803f8d42f9908d1af1c5540d4bb05fdfca8c1fa47decefc"
    hadolint_arch="arm64"
    hadolint_sha="331f1d3511b84a4f1e3d18d52fec284723e4019552f4f47b19322a53ce9a40ed"
    ;;
  *)
    echo "Unsupported platform for pinned binary tools: $(uname -s)/$(uname -m)" >&2
    exit 1
    ;;
esac

download() {
  local url="$1"
  local target="$2"
  local checksum="$3"
  if [[ -f "${target}" ]] && echo "${checksum}  ${target}" | sha256sum -c - >/dev/null 2>&1; then
    return
  fi
  curl --fail --location --silent --show-error "${url}" --output "${target}.tmp"
  echo "${checksum}  ${target}.tmp" | sha256sum -c -
  mv "${target}.tmp" "${target}"
}

shellcheck_archive="${CACHE}/shellcheck-v${SHELLCHECK_VERSION}.linux.${shellcheck_arch}.tar.gz"
download \
  "https://github.com/koalaman/shellcheck/releases/download/v${SHELLCHECK_VERSION}/$(basename "${shellcheck_archive}")" \
  "${shellcheck_archive}" \
  "${shellcheck_sha}"
extract_dir="$(mktemp -d "${CACHE}/shellcheck.XXXXXX")"
trap 'rm -rf "${extract_dir}"' EXIT
tar -xzf "${shellcheck_archive}" -C "${extract_dir}"
install -m 0755 \
  "${extract_dir}/shellcheck-v${SHELLCHECK_VERSION}/shellcheck" \
  "${BIN}/shellcheck"

hadolint_binary="${CACHE}/hadolint-linux-${hadolint_arch}"
download \
  "https://github.com/hadolint/hadolint/releases/download/v${HADOLINT_VERSION}/$(basename "${hadolint_binary}")" \
  "${hadolint_binary}" \
  "${hadolint_sha}"
install -m 0755 "${hadolint_binary}" "${BIN}/hadolint"

"${BIN}/shellcheck" --version | grep -F "version: ${SHELLCHECK_VERSION}" >/dev/null
"${BIN}/hadolint" --version | grep -F "Haskell Dockerfile Linter ${HADOLINT_VERSION}" >/dev/null
