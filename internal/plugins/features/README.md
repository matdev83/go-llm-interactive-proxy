# Feature plugins (official)

## Composition boundary

- **Registry:** [`internal/pluginreg`](../../pluginreg) registers a `FeatureFactory` per feature plugin id (see `RegisterFeature` on `Registry`). The factory receives opaque YAML (`yaml.Node`) and returns a versioned [`pkg/lipsdk/feature.FeatureBundle`](../../../pkg/lipsdk/feature/bundle.go) (hook chains plus optional `lipplugin.Lifecycle` values). Standard in-repo wiring still decodes YAML into `hooks.Config` in `features_install.go` and adapts with `pluginreg.FeatureFactoryFromHooks` so hook-only factories stay mechanical.
- **Feature packages** (`internal/plugins/features/<name>`) implement hook interfaces from `pkg/lipsdk/hooks`. They must not import `internal/core/runtime`, frontends, or backends. Wiring into HTTP or the executor stays in `cmd/` and `internal/pluginreg`.

## Constructor naming

Exported constructors that build hook implementations use **`New` + the hooks interface role** so call sites read like the assembled `hooks.Config` fields:

| Return type | Constructor name |
|-------------|-------------------|
| `SubmitHook` | `NewSubmitHook` |
| `RequestPartHook` | `NewRequestPartHook` |
| `ResponsePartHook` | `NewResponsePartHook` |
| `ToolReactor` | `NewToolReactor` |

- **Zero-config features** use the names above with no parameters (or defaults only).
- **Configured features** use the same names with a `(cfg <Package>Config)` argument. An extra variant is allowed when there are two entrypoints (e.g. `NewSubmitHook` and `NewSubmitHookWithConfig` for tests vs YAML-decoded config).

This matches the noop and reference plugins in this tree; out-of-repo feature plugins should follow the same pattern for consistency with `pluginreg` wiring.
