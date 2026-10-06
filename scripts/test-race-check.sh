#!/usr/bin/env bash
# Exercise race scan coverage and failure propagation without running Go tests.
set -euo pipefail
script_dir="$(cd "$(dirname "$0")" && pwd)"
bash "$script_dir/test-go-dev-guard.sh"
# Bypass the development-host guard in scripts/race-check.sh: this self-test
# exercises the scan logic with a stubbed toolchain and must run identically
# on every host, including blocked dev machines.
export LIP_ALLOW_RACE_ON_DEV=1
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/bin"
cat > "$fixture/bin/go" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  'env CGO_ENABLED') echo 1; exit 0 ;;
  'env CC') echo bash; exit 0 ;;
  'list ./...')
    printf '%s\n' example/internal/core/billing example/internal/core/runtime example/internal/archtest example/pkg/lipapi
    exit 0 ;;
esac
[[ " $* " == *' -c '* ]] && exit 0
printf '%s\n' "$*" >> "$SCAN_CALLS"
[[ -n "${FAIL_MATCH:-}" && " $* " == *"$FAIL_MATCH"* ]] && exit 17
exit 0
STUB
chmod +x "$fixture/bin/go"
export PATH="$fixture/bin:$PATH"
export SCAN_CALLS="$fixture/calls"
cd "$fixture"

verify_coverage() {
  local calls
  calls="$(cat "$SCAN_CALLS")"
  [[ "$(wc -l < "$SCAN_CALLS")" -eq 5 ]]
  for package in example/pkg/lipapi ./internal/core/runtime ./internal/archtest/...; do
    [[ "$(grep -F -- " $package" "$SCAN_CALLS" | wc -l)" -eq 1 ]]
  done
  [[ "$(grep -F -- ' ./internal/core/billing' "$SCAN_CALLS" | wc -l)" -eq 2 ]]
  [[ "$(grep -F -- ' -skip ^TestSupportAgreementShadowPredicate$' "$SCAN_CALLS" | wc -l)" -eq 1 ]]
  [[ "$(grep -F -- ' -run ^TestSupportAgreementShadowPredicate$' "$SCAN_CALLS" | wc -l)" -eq 1 ]]
  [[ "$(grep -F -- ' -timeout=60m -skip ^TestSupportAgreementShadowPredicate$' "$SCAN_CALLS" | wc -l)" -eq 1 ]]
  while IFS= read -r call; do
    [[ " $call " == *' -race '* && " $call " == *' -tags=precommit,integration '* && " $call " == *' -count=1 '* ]]
    [[ " $call " == *' -p=4 '* && " $call " == *' -parallel=4 '* ]]
  done <<< "$calls"
}

bash "$script_dir/race-check.sh" --strict > "$fixture/success.log" 2>&1
verify_coverage
cp "$SCAN_CALLS" "$fixture/all-calls"
for match in ' example/pkg/lipapi ' ' -skip ^TestSupportAgreementShadowPredicate$ ' ' -run ^TestSupportAgreementShadowPredicate$ ' ' ./internal/core/runtime ' ' ./internal/archtest/... '; do
  : > "$SCAN_CALLS"
  export FAIL_MATCH="$match"
  status=0
  bash "$script_dir/race-check.sh" --strict > "$fixture/failure.log" 2>&1 || status=$?
  [[ "$status" -eq 17 ]]
  verify_coverage
done
unset FAIL_MATCH
# Check the actual workflow matrix, so an omitted or duplicated job fails the
# coverage comparison even if the standalone script still covers every test.
mapfile -t workflow_lanes < <(sed -n 's/^[[:space:]]*lane: \[\(.*\)\]/\1/p' "$script_dir/../.github/workflows/race-fuzz-nightly.yml" | tr ',' '\n' | sed 's/^ *//;s/ *$//')
for lane in "${workflow_lanes[@]}"; do
  : > "$SCAN_CALLS"
  bash "$script_dir/race-check.sh" --strict --lane "$lane" > "$fixture/lane.log" 2>&1
  [[ "$(wc -l < "$SCAN_CALLS")" -eq 1 ]]
  grep -Fx -- "$(cat "$SCAN_CALLS")" "$fixture/all-calls" >/dev/null
  cat "$SCAN_CALLS" >> "$fixture/lane-calls"
done
diff <(sort "$fixture/all-calls") <(sort "$fixture/lane-calls")
if bash "$script_dir/race-check.sh" --strict --lane invalid > "$fixture/invalid.log" 2>&1; then
  echo 'Invalid lane unexpectedly succeeded' >&2
  exit 1
fi
echo 'Race scan coverage and failure propagation passed.'
