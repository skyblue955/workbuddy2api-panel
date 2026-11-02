#!/usr/bin/env bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
case "${1:-}" in
  --copy)
    cd "${2:?pass the isolated copy directory}"
    git apply --reverse --check "$HERE/DIFF_FILE.patch"
    git apply --reverse "$HERE/DIFF_FILE.patch"
    echo 'ROLLBACK PASS: baseline source restored in copy'
    ;;
  --commit)
    git -C "$HERE/.." revert --no-edit "${2:?pass the maintenance commit ID}"
    ;;
  *) echo 'Usage: ROLLBACK.sh --copy DIR | --commit COMMIT' >&2; exit 2 ;;
esac
