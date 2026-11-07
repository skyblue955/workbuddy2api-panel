#!/usr/bin/env bash
set -euo pipefail
ROOT=/Users/jinyanya/Code/Codex/workbuddy
ART=$ROOT/maintenance/v9-original-oneclick
export PATH=/Users/jinyanya/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.3.darwin-arm64/bin:/Users/jinyanya/.local/share/fnm/node-versions/v22.21.1/installation/bin:$PATH GOTOOLCHAIN=local GOCACHE=/private/tmp/workbuddy-gocache GOMODCACHE=/Users/jinyanya/go/pkg/mod GOPROXY=off
BASE=$(cat "$ART/evidence/baseline-path.txt")
cp "$ART/REGRESSION_INPUT.go.txt" "$BASE/internal/panel/autotask_ready_skip_test.go"
set +e
(cd "$BASE"; go test -race ./internal/panel -run '^TestAccountTaskAutoReadyModelChatRouteSkipsRealAction$' -count=1 -v) > "$ART/evidence/baseline-regression.log" 2>&1
code=$?
set -e
printf 'BASELINE_REGRESSION_EXIT=%s\n' "$code" >> "$ART/evidence/baseline-regression.log"
rm "$BASE/internal/panel/autotask_ready_skip_test.go"
[[ "$code" == 1 ]]
CLEAN=$(mktemp -d /private/tmp/workbuddy-v9-clean.XXXXXX)
printf '%s\n' "$CLEAN" > "$ART/evidence/clean-path.txt"
git -C "$ROOT" archive c4fd2abfe2145e8a17a2d03acdda05fb84f0b2dd | tar -x -C "$CLEAN"
(cd "$CLEAN"; git apply --check "$ART/DIFF_FILE.patch"; git apply "$ART/DIFF_FILE.patch"; bash scripts/verify.sh) > "$ART/evidence/clean-verify.log" 2>&1
printf 'CLEAN_VERIFY_EXIT=0\n' >> "$ART/evidence/clean-verify.log"
bash "$ART/ROLLBACK.sh" "$CLEAN" > "$ART/evidence/rollback-verify.log" 2>&1
(cd "$CLEAN"; bash scripts/verify.sh) >> "$ART/evidence/rollback-verify.log" 2>&1
printf 'ROLLBACK_VERIFY_EXIT=0\n' >> "$ART/evidence/rollback-verify.log"
cp "$ART/REGRESSION_INPUT.go.txt" "$CLEAN/internal/panel/autotask_ready_skip_test.go"
set +e
(cd "$CLEAN"; go test -race ./internal/panel -run '^TestAccountTaskAutoReadyModelChatRouteSkipsRealAction$' -count=1 -v) > "$ART/evidence/rollback-regression.log" 2>&1
code=$?
set -e
printf 'ROLLBACK_REGRESSION_EXIT=%s\n' "$code" >> "$ART/evidence/rollback-regression.log"
rm "$CLEAN/internal/panel/autotask_ready_skip_test.go"
[[ "$code" == 1 ]]
shasum -a 256 -c "$ART/MODIFIED_SHA256.txt" > "$ART/evidence/maintained-hashes.log"
echo 'ISOLATED_GATES_PASS'
