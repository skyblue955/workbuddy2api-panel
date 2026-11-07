#!/usr/bin/env bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
if [[ $# != 1 || "$1" != /* || ! -d "$1/internal/upstream" ]]; then
  echo 'Usage: ROLLBACK.sh /absolute/path/to/isolated-source-copy' >&2
  exit 2
fi
cd "$1"
git apply --reverse --check "$HERE/DIFF_FILE.patch"
git apply --reverse "$HERE/DIFF_FILE.patch"
echo 'ROLLBACK PASS: v2 API/rewards code restored to b8522fc'
