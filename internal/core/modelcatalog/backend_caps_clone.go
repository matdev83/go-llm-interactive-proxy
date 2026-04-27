package modelcatalog

import (
	"maps"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// CloneBackendCaps returns a shallow copy of caps for safe in-place mutation, or nil when caps is nil.
func CloneBackendCaps(b lipapi.BackendCaps) lipapi.BackendCaps {
	if b == nil {
		return nil
	}
	return maps.Clone(b)
}
