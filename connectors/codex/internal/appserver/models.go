package appserver

import (
	"errors"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ErrUnknownModel is returned when a route model is not auto and is not present
// in the active provider allowlist (CanonicalID or NativeID).
var ErrUnknownModel = errors.New("appserver: unknown model")

func codexCanonicalFallback(native string) string {
	return vendorPrefix + "/" + native
}

// resolveAllowedModel resolves the route/config model to the allowlisted NativeID.
// auto is always allowed.
func (s *codexSpec) resolveAllowedModel(call *lipapi.Call) (string, error) {
	if s == nil {
		return "", ErrUnknownModel
	}
	model := s.cfg.Model
	if m := acp.CallRouteModel(call, "codex.model"); m != "" {
		model = stripOpenAIModelPrefix(m)
	}
	if isAutoModel(model) {
		return autoModelSentinel, nil
	}
	model = strings.TrimSpace(model)
	if s.index.IsKnownNative(model) {
		return model, nil
	}
	if native, ok := s.index.NativeForCanonical(vendorPrefix + "/" + model); ok {
		return native, nil
	}
	if native, ok := s.index.NativeForCanonical(model); ok {
		return native, nil
	}
	return "", ErrUnknownModel
}
