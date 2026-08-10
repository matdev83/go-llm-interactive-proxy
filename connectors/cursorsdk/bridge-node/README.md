# lip-cursor-sdk-bridge

Project-owned Node companion for the Go-LIP `cursorsdk` backend.

Operator documentation: [`docs/cursor-sdk-backend.md`](../../../../../docs/cursor-sdk-backend.md).

## Pins

- `@cursor/sdk` exact `1.0.23`
- Node `>=22.13`

## Layout

- `bin/lip-cursor-sdk-bridge.js` — executable entry (`--version`, `doctor`, or NDJSON server)
- `src/protocol.ts` — versioned bounded NDJSON bridge contract (shared fixtures with Go)
- `src/sdkMock.ts` — deterministic SDK mock for default tests
- `src/liveProbe.ts` — opt-in live probe (`CURSOR_SDK_LIVE_PROBE=1` + `CURSOR_API_KEY`)
- `src/liveScenarios.ts` — opt-in live scenarios (`CURSOR_SDK_LIVE=1` + `CURSOR_API_KEY`)

Shared fixtures live in `../internal/product/testdata/fixtures` and are consumed by both Go and TypeScript tests.

## Release smoke (repo root)

```bash
make test-cursor-sdk-platform            # current OS; fake bridge lifecycle; no API key
make test-cursor-sdk-live                # opt-in real SDK Node scenarios; BLOCKED exit 0 without flag/key
make test-cursor-sdk-live-bridge         # opt-in Open/RunStream lifecycle (-tags=cursorsdk_live_bridge); BLOCKED exit 0 without flag/key
make test-cursor-sdk-comparison-report   # ACP vs SDK matrix (synthetic/blocked offline)
```

## Commands

```bash
npm ci
npm run typecheck
npm test
npm run build
npm pack --dry-run
```

`npm test` auto-discovers all `src/*.test.ts` files via Node's test-runner glob, so new test files are picked up without editing `package.json`.

Safe local checks (no credentials, no agent creation):

```bash
node bin/lip-cursor-sdk-bridge.js --version
node bin/lip-cursor-sdk-bridge.js doctor
```

Live probe (optional, not part of default tests):

```bash
set CURSOR_SDK_LIVE_PROBE=1
set CURSOR_API_KEY=...
npm run live-probe
```

## Installation policy

Go-LIP never runs `npm install`, `npm ci`, or other package lifecycle scripts at config validation, startup, model discovery, or request handling. Operators (or CI) install and build this companion package separately, then point `bridge_executable` at the resulting binary on `PATH` or an explicit path. The package declares no `preinstall` / `install` / `postinstall` product hooks.
