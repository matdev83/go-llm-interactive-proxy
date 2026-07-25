# PR D4 release-gate results

Recorded on host described in `measurement-host.txt`.
Measurement parent SHA: `e80198596ba8d130589e6061e8f5c31af340084b`

| # | Command | Started (UTC) | Finished (UTC) | Exit |
| --- | --- | --- | --- | ---: |
| 1 | `make test` | 2026-07-25T21:17:17Z | 2026-07-25T21:20:00Z | 0 |
| 2 | `make test-race` | 2026-07-25T21:20:00Z | 2026-07-25T21:26:34Z | 0 |
| 3 | `make test-fuzz` | 2026-07-25T21:26:34Z | 2026-07-25T21:32:14Z | 0 |
| 4 | `make quality-checks` | 2026-07-25T21:32:14Z | 2026-07-25T21:32:40Z | 0 |
| 5 | `make arch-report` | 2026-07-25T21:32:40Z | 2026-07-25T21:32:40Z | 0 |
| 6 | `make bench` | 2026-07-25T21:32:40Z | 2026-07-25T21:36:32Z | 0 |
| 7 | `go test -tags=precommit -run '^TestRuntimeConfigReloadSoak$' -count=1 -v ./internal/stdhttp/...` | 2026-07-25T21:36:32Z | 2026-07-25T21:36:39Z | 0 |
| 8 | `go mod verify` | 2026-07-25T21:36:39Z | 2026-07-25T21:36:50Z | 0 |
| 9 | `go vet ./...` | 2026-07-25T21:36:50Z | 2026-07-25T21:36:52Z | 0 |
| 10 | `golangci-lint run --timeout=10m` | 2026-07-25T21:36:52Z | 2026-07-25T21:37:20Z | 1 |
| 11 | `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` | 2026-07-25T21:37:20Z | 2026-07-25T21:37:34Z | 0 |
| 12 | `(cd testdata/enterprise_module && GOWORK=off go test ./... -count=1)` | 2026-07-25T21:37:34Z | 2026-07-25T21:37:36Z | 0 |

## Notes

- Raw combined stdout/stderr: workspace `/tmp/prd-gates.log` (not committed).
- `golangci-lint` outcome is the exit code above (no waiver).
- No provider credentials or live provider calls were used.


## golangci-lint follow-up (PR D packages)

After fixing `modernize` min/max findings in `internal/qa/task84_legacy_absent_docs_test.go` and `internal/qa/task91_architecture_docs_test.go`:

- `golangci-lint run --timeout=5m ./internal/qa/...` → exit **0** (no findings in packages changed by PR D).
- Full `golangci-lint run --timeout=10m` still exits **1** with **78 pre-existing** findings outside PR D changed packages (forcetypeassert, gofumpt, modernize, staticcheck QF1011, thelper, paralleltest, etc.). Not introduced by PR D. No waiver for packages changed by this work.
