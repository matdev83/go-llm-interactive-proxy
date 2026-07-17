package secretsguard

import "github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"

// ComposeSource selects the secret source policy from effective access mode and
// feature enablement. Disabled performs no environment access. Multi-user never
// calls env (D4). Feature plugin YAML parsing remains Phase 4; callers pass
// composition-neutral options.
func ComposeSource(mode accessmode.Mode, featureEnabled bool, env Environment, opts SingleUserOptions) (Source, error) {
	if !featureEnabled {
		return NewDisabledSource(), nil
	}
	switch mode {
	case accessmode.ModeMultiUser:
		return NewMultiUserSource(env)
	default:
		return NewSingleUserSource(env, opts)
	}
}
