#!/usr/bin/env bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
if [[ $# != 1 || "$1" != /* || ! -d "$1/internal/panel" ]]; then
 echo 'Usage: ROLLBACK.sh /absolute/path/to/isolated-source-copy' >&2
 exit 2
fi
cd "$1"
git apply --reverse --check "$HERE/DIFF_FILE.patch"
git apply --reverse "$HERE/DIFF_FILE.patch"
echo 'ROLLBACK PASS: v3 claim-only code restored to 93ce125'
