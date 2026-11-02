#!/usr/bin/env bash
# Deterministic local/CI acceptance gate; no real credentials or account calls.
set -euo pipefail
cd "$(dirname "$0")/.."
command -v node >/dev/null || { echo 'Node is required for frontend smoke tests' >&2; exit 1; }
node --check internal/panel/app.js
node --check internal/panel/mobile.js
bash -n login.sh signin.sh credit.sh scripts/verify.sh
go vet ./...
go test -race ./...
go build ./...
echo 'ACCEPTANCE PASS'
