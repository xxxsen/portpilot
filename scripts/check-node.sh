#!/usr/bin/env bash
set -euo pipefail

node <<'JS'
const version = process.versions.node;
const match = /^24\.(\d+)\.(\d+)$/.exec(version);
if (!match || Number(match[1]) < 22 || (Number(match[1]) === 22 && Number(match[2]) < 1)) {
  console.error(`Node.js >=24.22.1 <25 is required (CVE-2026-111497); found ${version}`);
  process.exit(1);
}
JS
