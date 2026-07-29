package execbackend

import (
	"encoding/json"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// CodexIgnoreUnsupportedGenParamsExt is the canonical-call extension key (bool).
// When true, temperature, top_p, and max_output_tokens are dropped instead of
// failing payload build; used by codex-client-compat for OpenCode and similar clients.
const CodexIgnoreUnsupportedGenParamsExt = "openai_codex.ignore_unsupported_gen_params"

// CanEnforceAuthorityMaxOutputTokens reports whether the backend can represent a
// MaxOutputTokens authority clamp on the wire for this call.
func (be Backend) CanEnforceAuthorityMaxOutputTokens(call *lipapi.Call) bool {
	if call == nil || call.Options.MaxOutputTokens == nil {
		return true
	}
	if !be.EnforcesMaxOutputTokens {
		return false
	}
	if be.IgnoresAuthorityMaxOutputTokensClamp != nil && be.IgnoresAuthorityMaxOutputTokensClamp(*call) {
		return false
	}
	return true
}

// IgnoresClampViaCodexUnsupportedGenParams returns true when the codex-client-compat
// extension requests dropping unsupported generation parameters including max output.
func IgnoresClampViaCodexUnsupportedGenParams(call lipapi.Call) bool {
	ignore, ok := callExtensionBool(call, CodexIgnoreUnsupportedGenParamsExt)
	return ok && ignore
}

func callExtensionBool(call lipapi.Call, key string) (bool, bool) {
	if len(call.Extensions) == 0 {
		return false, false
	}
	raw, ok := call.Extensions[key]
	if !ok {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}
