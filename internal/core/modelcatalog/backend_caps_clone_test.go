package modelcatalog_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCloneBackendCaps_nilAndCopy(t *testing.T) {
	t.Parallel()
	if modelcatalog.CloneBackendCaps(nil) != nil {
		t.Fatal("nil in should be nil out")
	}
	orig := lipapi.BackendCaps{
		lipapi.CapabilityTools: {},
	}
	cp := modelcatalog.CloneBackendCaps(orig)
	delete(cp, lipapi.CapabilityTools)
	if _, ok := orig[lipapi.CapabilityTools]; !ok {
		t.Fatal("mutating clone should not affect original map")
	}
}
