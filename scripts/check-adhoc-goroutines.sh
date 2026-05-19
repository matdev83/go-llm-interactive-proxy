#!/usr/bin/env bash
# Enforce a tiny allowlist of explicit `go` statements in non-test code.
# Run from repository root. Requires ripgrep (rg).

set -euo pipefail

if ! command -v rg >/dev/null 2>&1; then
	echo "check-adhoc-goroutines: ripgrep (rg) not found; install rg or skip in CI where guaranteed."
	exit 0
fi

mapfile -t hits < <(
	rg --files-with-matches --glob '!*_test.go' '^\s+go\s' internal pkg cmd 2>/dev/null | sed 's#\\#/#g' | sort -u || true
)

bad=()
for f in "${hits[@]}"; do
	case "$f" in
	internal/stdhttp/server.go | internal/core/stream/keepalive.go | internal/plugins/frontends/holdalive/wait.go | internal/infra/runtimebundle/modelcatalog_refresh_loop.go) ;;
	*) bad+=("$f") ;;
	esac
done

if [[ ${#bad[@]} -gt 0 ]]; then
	echo "ERROR: disallowed explicit goroutine spawn in non-test code (use long-lived workers / stream pumps; update allowlist in scripts/check-adhoc-goroutines.* only when intentional):"
	printf '  %s\n' "${bad[@]}"
	exit 1
fi

echo "OK: ad-hoc goroutine allowlist check passed (${#hits[@]} allowed file(s))"
