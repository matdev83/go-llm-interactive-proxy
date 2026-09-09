package runtimebundle_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/featurehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/reasoninghost"
)

type testHostPolicy struct {
	action reasoninghost.EgressAction
}

func (p testHostPolicy) Decide(_ context.Context, _ reasoninghost.EgressInput) (reasoninghost.EgressDecision, error) {
	return reasoninghost.EgressDecision{
		Action:        p.action,
		PolicyVersion: "v1",
	}, nil
}

func TestBuildHost_WithFeatureHostRegistrations_ReasoningBinding(t *testing.T) {
	t.Parallel()

	cfgPath := runtimebundle.MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))

	binding := &reasoninghost.Binding{
		EgressPolicies: map[string]reasoninghost.EgressPolicy{
			"test-egress": testHostPolicy{action: reasoninghost.EgressAllow},
		},
	}

	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath: cfgPath,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
		Production: runtimebundle.ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{
				binding.Registration(),
			},
		},
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost with FeatureHostRegistrations failed: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })
}
