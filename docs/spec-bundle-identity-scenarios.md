# Identity scenario registry (specification bundle)

Stable identifiers for **proxy identity** (issue #147): A-leg `Server`, B-leg `User-Agent`, OpenRouter attribution, connector allowlist/exclusions, and B2BUA isolation. Each row maps to `SpecBundleIdentityScenarios()` in [`spec_bundle_scenarios.go`](../internal/core/identity/spec_bundle_scenarios.go).

| ID | Invariant (summary) | Primary test |
|----|---------------------|--------------|
| `SB-IDENTITY-default-upstream` | Omitted/proxy User-Agent emits literal LIP product identity on the B-leg wire (ID-001). | `TestTransport_modesOnWire` |
| `SB-IDENTITY-ua-passthrough` | Passthrough forwards validated call-path User-Agent verbatim; missing/invalid call-path omits (ID-010). | `TestTransport_modesOnWire` |
| `SB-IDENTITY-backend-override` | Backend nested identity override wins over proxy-wide User-Agent policy for approved connectors. | `TestIdentityTransport_approvedFactoriesWireUserAgent` |
| `SB-IDENTITY-drop-suppresses` | Drop suppresses User-Agent including Go's Go-http-client/1.1 default (ID-030). | `TestTransport_dropSuppressesBareGoDefault` |
| `SB-IDENTITY-openrouter-attr` | OpenRouter proxy defaults emit LIP HTTP-Referer and X-OpenRouter-Title, overriding captured client values. | `TestOpenRouterAttribution_defaultProxyOverridesCapturedClient` (`connectors/openrouter`) |
| `SB-IDENTITY-aleg-server` | A-leg Server defaults to literal LIP product identity when config is nil/omitted (ID-050). | `TestDownstreamServerMiddleware_nilConfigProxyLiteral` |
| `SB-IDENTITY-exclusions` | Approved and excluded connector ID lists are literal, disjoint, and locked (ID-147-ALLOW). | `TestIdentityTransport_ID147_allowlistAndExclusionLiterals` |
| `SB-IDENTITY-b2bua-failover` | Ordered failover applies each backend's effective User-Agent policy independently (ID-147-FO). | `TestIdentityExecutor_ID147_orderedFailoverIsolatesUserAgent` |
| `SB-IDENTITY-b2bua-parallel` | Parallel race candidates isolate User-Agent header state per B-leg (ID-147-PR). | `TestIdentityExecutor_ID147_parallelRaceIsolatesUserAgent` |
| `SB-IDENTITY-no-failover-after-output` | Post-output failures do not open an identity-bearing failover backend (ID-147-PO). | `TestIdentityExecutor_ID147_noFailoverAfterOutputPreservesIdentityChoice` |

Operator guide: [proxy-identity.md](proxy-identity.md).

When adding or splitting tests, update `spec_bundle_scenarios.go` (including repository-relative `PackageRel`, which may point under `connectors/`), this table, and keep `TestSpecBundle_identityScenarios_referenceTests` passing (`go test -tags=precommit ./internal/core/identity/...`). The reference harness resolves tests via on-disk `*_test.go` under the repo root and does not require the package to be in the root module/`go.work`.
