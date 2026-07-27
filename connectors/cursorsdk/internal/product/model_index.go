package product

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func resolveNativeFromCandidate(index *acp.ModelIndex, call lipapi.Call, cand AttemptCandidate) (string, bool) {
	identity := strings.TrimSpace(cand.Primary.Model)
	if identity == "" {
		identity = strings.TrimSpace(acp.CallRouteModel(&call, "acp.model"))
	}
	if identity == "" || index == nil {
		return "", false
	}
	if index.IsKnownNative(identity) {
		return identity, true
	}
	if native, ok := index.NativeForCanonical(identity); ok {
		return native, true
	}
	if !strings.HasPrefix(identity, vendorPrefix+"/") {
		if native, ok := index.NativeForCanonical(vendorPrefix + "/" + identity); ok {
			return native, true
		}
	}
	return "", false
}

func resolveCaps(catalog *Catalog, index *acp.ModelIndex, call lipapi.Call, cand AttemptCandidate) lipapi.BackendCaps {
	native, ok := resolveNativeFromCandidate(index, call, cand)
	if !ok {
		return lipapi.BackendCaps{}
	}
	if catalog == nil {
		return lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	}
	return catalog.CapsFor(native)
}
