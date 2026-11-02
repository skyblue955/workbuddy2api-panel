#!/usr/bin/env bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
case "${1:-}" in
 --copy) cd "${2:?isolated copy required}"; git apply -R --check "$HERE/DIFF_FILE.patch"; git apply -R "$HERE/DIFF_FILE.patch"; echo 'ROLLBACK PASS: phase1 source restored';;
 --commit) git -C "$HERE/../.." revert --no-edit "${2:?v1 commit required}";;
 *) echo 'Usage: ROLLBACK.sh --copy DIR | --commit COMMIT' >&2; exit 2;;
esac
