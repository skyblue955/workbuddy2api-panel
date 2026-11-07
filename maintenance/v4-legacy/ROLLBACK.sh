#!/usr/bin/env bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
if [[ $# != 1 || "$1" != /* || ! -d "$1/internal/panel" ]]; then
 echo 'Usage: ROLLBACK.sh /absolute/path/to/isolated-source-copy' >&2
 exit 2
fi
TARGET=$(cd "$1" && pwd)
if [[ "$TARGET" == "$(cd "$HERE/../.." && pwd)" ]]; then
 echo 'Use an isolated copy; this script preserves the maintained worktree.' >&2
 exit 2
fi
cd "$TARGET"
git apply --reverse --check "$HERE/DIFF_FILE.patch"
git apply --reverse "$HERE/DIFF_FILE.patch"
echo 'ROLLBACK PASS: v4 reward integrity code restored to ec78db5'
