package lipsdk_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestStandardDistributionRequirements_nonEmpty(t *testing.T) {
	t.Parallel()
	req := lipsdk.StandardDistributionRequirements()
	if len(req) < 8 {
		t.Fatalf("expected multiple requirements, got %d", len(req))
	}
}
