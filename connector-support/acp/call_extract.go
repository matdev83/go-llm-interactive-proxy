package acp

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// CallRouteModel extracts the effective model from a canonical call's route
// selector (format "backend:model", or a bare model), falling back to the
// per-vendor model extension key when the selector is absent. The selector
// takes precedence so route-resolved models override request extensions.
//
// modelExtKey is the vendor-specific extension key (e.g. "acp.model",
// "codex.model") consulted only when Route.Selector is empty/whitespace.
func CallRouteModel(call *lipapi.Call, modelExtKey string) string {
	if call == nil {
		return ""
	}
	selector := strings.TrimSpace(call.Route.Selector)
	if selector != "" {
		if _, after, ok := strings.Cut(selector, ":"); ok {
			return strings.TrimSpace(after)
		}
		return selector
	}
	if call.Extensions != nil {
		if raw, ok := call.Extensions[modelExtKey]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// CallClientSession extracts the client session ID from a canonical call,
// preferring Session.ClientSessionID, then the "session.id" extension key,
// then "default". This is the shared extraction used by both the ACP CLI
// subprocess connectors and the Codex app-server backend.
func CallClientSession(call *lipapi.Call) string {
	if call == nil {
		return "default"
	}
	if sid := strings.TrimSpace(call.Session.ClientSessionID); sid != "" {
		return sid
	}
	if call.Extensions != nil {
		if raw, ok := call.Extensions["session.id"]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				if sid := strings.TrimSpace(s); sid != "" {
					return sid
				}
			}
		}
	}
	return "default"
}
