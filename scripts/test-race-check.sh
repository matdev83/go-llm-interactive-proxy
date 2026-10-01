#!/usr/bin/env bash
# Exercise race scan coverage and failure propagation without running Go tests.
set -euo pipefail
script_dir="$(cd "$(dirname "$0")" && pwd)"
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
[[ -n "${FAIL_PACKAGE:-}" && " $* " == *" $FAIL_PACKAGE "* ]] && exit 17
exit 0
STUB
chmod +x "$fixture/bin/go"
export PATH="$fixture/bin:$PATH"
export SCAN_CALLS="$fixture/calls"
cd "$fixture"

verify_coverage() {
  local calls
  calls="$(cat "$SCAN_CALLS")"
  [[ "$(wc -l < "$SCAN_CALLS")" -eq 4 ]]
  for package in example/pkg/lipapi ./internal/core/billing ./internal/core/runtime ./internal/archtest/...; do
    [[ "$(grep -F -- " $package" "$SCAN_CALLS" | wc -l)" -eq 1 ]]
  done
  while IFS= read -r call; do
    [[ " $call " == *' -race '* && " $call " == *' -tags=precommit,integration '* && " $call " == *' -count=1 '* ]]
    [[ " $call " == *' -p=4 '* && " $call " == *' -parallel=4 '* ]]
  done <<< "$calls"
}

bash "$script_dir/race-check.sh" --strict > "$fixture/success.log" 2>&1
verify_coverage
for package in example/pkg/lipapi ./internal/core/billing ./internal/core/runtime ./internal/archtest/...; do
  : > "$SCAN_CALLS"
  export FAIL_PACKAGE="$package"
  status=0
  bash "$script_dir/race-check.sh" --strict > "$fixture/failure.log" 2>&1 || status=$?
  [[ "$status" -eq 17 ]]
  verify_coverage
done
echo 'Race scan coverage and failure propagation passed.'
