#!/usr/bin/env bash
# Reverse this correction only in the explicitly selected source tree.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
if [[ $# != 1 || ! -d "$1/internal/panel" ]]; then
  echo 'Usage: ROLLBACK.sh /absolute/path/to/source-tree' >&2
  exit 2
fi
cd "$1"
git apply --reverse --check "$HERE/DIFF_FILE.patch"
git apply --reverse "$HERE/DIFF_FILE.patch"
echo 'ROLLBACK PASS: source-hint correction reversed'
