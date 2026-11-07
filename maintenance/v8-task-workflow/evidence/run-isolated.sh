#!/usr/bin/env bash
set -euo pipefail
ROOT=/Users/jinyanya/Code/Codex/workbuddy
ART=$ROOT/maintenance/v8-task-workflow
export PATH=/Users/jinyanya/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.3.darwin-arm64/bin:/Users/jinyanya/.local/share/fnm/node-versions/v22.21.1/installation/bin:$PATH GOTOOLCHAIN=local GOCACHE=/private/tmp/workbuddy-gocache GOMODCACHE=/Users/jinyanya/go/pkg/mod GOPROXY=off
BASE=$(cat "$ART/evidence/baseline-path.txt")
cp "$ART/ROUTE_PROBE.txt" "$BASE/internal/panel/v8_route_probe_test.go"
(cd "$BASE"; go test ./internal/panel -run '^TestV8RouteProbe$' -count=1 -v) > "$ART/evidence/baseline-probe.log" 2>&1
rm "$BASE/internal/panel/v8_route_probe_test.go"
CLEAN=$(mktemp -d /private/tmp/workbuddy-v8-clean.XXXXXX)
printf '%s\n' "$CLEAN" > "$ART/evidence/clean-path.txt"
git -C "$ROOT" archive 9c8b77a07e1f920de238f72092890b3d9aeb9d24 | tar -x -C "$CLEAN"
(cd "$CLEAN"; git apply --check "$ART/DIFF_FILE.patch"; git apply "$ART/DIFF_FILE.patch"; bash scripts/verify.sh) > "$ART/evidence/clean-verify.log" 2>&1
printf 'CLEAN_VERIFY_EXIT=0\n' >> "$ART/evidence/clean-verify.log"
cp "$ART/ROUTE_PROBE.txt" "$CLEAN/internal/panel/v8_route_probe_test.go"
cp "$ART/EMPTY_FUNCTION_PROBE.txt" "$CLEAN/internal/panel/v8_empty_function_probe_test.go"
(cd "$CLEAN"; go test ./internal/panel -run '^TestV8(RouteProbe|EmptyFunctionProbe)$' -count=1 -v) > "$ART/evidence/modified-probe.log" 2>&1
rm "$CLEAN/internal/panel/v8_route_probe_test.go" "$CLEAN/internal/panel/v8_empty_function_probe_test.go"
bash "$ART/ROLLBACK.sh" "$CLEAN" > "$ART/evidence/rollback-verify.log" 2>&1
(cd "$CLEAN"; bash scripts/verify.sh) >> "$ART/evidence/rollback-verify.log" 2>&1
cp "$ART/ROUTE_PROBE.txt" "$CLEAN/internal/panel/v8_route_probe_test.go"
cp "$ART/EMPTY_FUNCTION_PROBE.txt" "$CLEAN/internal/panel/v8_empty_function_probe_test.go"
(cd "$CLEAN"; go test ./internal/panel -run '^TestV8(RouteProbe|EmptyFunctionProbe)$' -count=1 -v) >> "$ART/evidence/rollback-verify.log" 2>&1
rm "$CLEAN/internal/panel/v8_route_probe_test.go" "$CLEAN/internal/panel/v8_empty_function_probe_test.go"
printf 'ROLLBACK_VERIFY_EXIT=0\n' >> "$ART/evidence/rollback-verify.log"
printf 'ISOLATED_GATES_PASS\n'
