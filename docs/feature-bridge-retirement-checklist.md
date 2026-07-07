# Feature bridge retirement checklist

This document tracks the conversion of hook-only feature factories to native `FeatureBundle` and the retirement of the `FeatureFactoryFromHooks` bridge.

## Converted to native FeatureBundle (7/7)

| Feature ID | Package | Was hook-only | Now native | Hooks wired |
| --- | --- | --- | --- | --- |
| `submit-noop` | `submitnoop` | yes | yes | `SubmitHooks` + optional `Lifecycles` |
| `parts-noop` | `partsnoop` | yes | yes | `RequestPartHooks` + `ResponsePartHooks` |
| `tool-reactor-noop` | `toolreactornoop` | yes | yes | `ToolReactors` |
| `ref-submit-annotate` | `refsubmit` | yes | yes | `SubmitHooks` |
| `ref-request-suffix` | `refparts` | yes | yes | `RequestPartHooks` + `ResponsePartHooks` |
| `ref-tool-prefix` | `reftool` | yes | yes | `ToolReactors` |
| `codex-client-compat` | `codexclientcompat` | yes | yes | `RequestPartHooks` |

## Already native FeatureBundle (6/6, no change)

| Feature ID | Package | Contribution |
| --- | --- | --- |
| `ref-autoappend-file` | `refautoappend` | `SessionOpeners` + `RequestTransforms` |
| `ref-tool-policy` | `reftoolpolicy` | `ToolCatalogFilters` + `ToolCallPolicies` + `ToolReactors` |
| `ref-workspace-guard` | `refworkspaceguard` | `WorkspaceResolvers` + `RequestTransforms` + `ToolCatalogFilters` + `ToolReactors` |
| `ref-traffic-transcript` | `reftraffictranscript` | `TrafficObservers` + `UsageObservers` + `RawCaptureSinks` + `TrafficRedactors` |
| `ref-verifier-stub` | `refverifier` | `CompletionGates` |
| `pre-request-policy` | `prerequestpolicy` | `PreRequestHandlers` |

## Bridge status

`FeatureFactoryFromHooks` (formerly in `internal/featurebundle/featurebundle.go`) has been **deleted**. All 13 bundled features now return `lipfeature.FeatureBundle` directly. The `internal/core/hooks` import has been removed from `internal/standardplugins` and `internal/featurebundle`.

The `internal/featurebundle` package now contains only the merge surface (`MergeFeatureSurface` + `MergedFeatureSurface` with SDK hook slices). `BuildFeatureHooks` and `hooks.New` live in `internal/infra/runtimebundle` (composition root).
