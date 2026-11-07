#!/usr/bin/env bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
if [[ $# != 1 || "$1" != /* || ! -d "$1/internal/panel" ]]; then echo 'Usage: ROLLBACK.sh /absolute/path/to/isolated-source-copy' >&2; exit 2; fi
TARGET=$(cd "$1" && pwd)
if [[ "$TARGET" == "$(cd "$HERE/../.." && pwd)" ]]; then echo 'Use an isolated copy; maintained worktree is preserved.' >&2; exit 2; fi
cd "$TARGET"
git apply --reverse --check "$HERE/DIFF_FILE.patch"
git apply --reverse "$HERE/DIFF_FILE.patch"
while read -r hash file; do
 if [[ "$hash" == ABSENT ]]; then [[ ! -e "$file" ]]; else actual=$(shasum -a 256 "$file"); [[ "${actual%% *}" == "$hash" ]]; fi
done < "$HERE/ORIGINAL_SHA256.txt"
echo 'ROLLBACK PASS: c4fd2ab source restored; 2 hashes/absences matched'
