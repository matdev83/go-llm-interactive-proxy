package extensions_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

type snapshotControlToolProvider struct {
	id      string
	handles int
}

func (p *snapshotControlToolProvider) ID() string { return p.id }

func (p *snapshotControlToolProvider) Spec() controltool.Spec {
	return controltool.Spec{
		Tool: lipapi.ToolDef{
			Name:        "proxy_control",
			Description: "Call this only when the assigned proxy-local control action is complete.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"note": {
						"type": "string",
						"description": "Bounded control result."
					}
				},
				"required": ["note"],
				"additionalProperties": false
			}`),
		},
		Instruction: controltool.Instruction{
			Role: lipapi.RoleSystem,
			Text: "Call proxy_control only when the assigned work is complete.",
		},
		MaxArgsBytes: controltool.DefaultMaxArgsBytes,
	}
}

func (p *snapshotControlToolProvider) Handle(context.Context, controltool.CompletedCall, controltool.Meta) (controltool.Outcome, error) {
	p.handles++
	return controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "done", ReasonCode: "complete"}, nil
}

func snapshotWithControlTool(t *testing.T, provider controltool.Provider) *extensions.RequestRuntimeSnapshot {
	t.Helper()

	cs := lipfeature.NewContributionSet()
	if provider != nil {
		require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneControlToolProvider, "plugin-1", provider))
	}
	return extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{FeaturePlanes: cs.Freeze()})
}

// TestRequestRuntimeSnapshot_ControlToolProvider pins the snapshot accessor: a
// generation-admitted provider reads back from the immutable request snapshot,
// and a generation without one reads nil without any provider method call.
func TestRequestRuntimeSnapshot_ControlToolProvider(t *testing.T) {
	t.Parallel()

	t.Run("nil_receiver_returns_zero_value", func(t *testing.T) {
		t.Parallel()

		var snap *extensions.RequestRuntimeSnapshot
		assert.Nil(t, snap.ControlToolProvider())
	})

	t.Run("absent_provider_is_nil_without_provider_calls", func(t *testing.T) {
		t.Parallel()

		snap := snapshotWithControlTool(t, nil)
		assert.Nil(t, snap.ControlToolProvider(), "absent control-tool plane must read nil")
	})

	t.Run("admitted_provider_reads_back", func(t *testing.T) {
		t.Parallel()

		provider := &snapshotControlToolProvider{id: "proxy.control.example"}
		snap := snapshotWithControlTool(t, controltool.Provider(provider))

		got := snap.ControlToolProvider()
		require.NotNil(t, got, "occupied control_tool_provider must read back")
		assert.Equal(t, "proxy.control.example", got.ID())
		assert.Zero(t, provider.handles, "snapshot construction and access must not invoke the provider")
	})
}

// TestRequestRuntimeSnapshot_ControlToolProviderRemovalIsGenerationScoped proves
// a newly built snapshot without the provider does not disturb previously built
// occupied snapshots (requirement 10.5).
func TestRequestRuntimeSnapshot_ControlToolProviderRemovalIsGenerationScoped(t *testing.T) {
	t.Parallel()

	provider := &snapshotControlToolProvider{id: "proxy.control.example"}
	occupied := snapshotWithControlTool(t, controltool.Provider(provider))
	require.NotNil(t, occupied.ControlToolProvider())

	withdrawn := snapshotWithControlTool(t, nil)
	assert.Nil(t, withdrawn.ControlToolProvider(), "new generation must not inherit the removed provider")

	got := occupied.ControlToolProvider()
	require.NotNil(t, got, "previously admitted snapshot must remain occupied")
	assert.Equal(t, "proxy.control.example", got.ID())
	assert.Zero(t, provider.handles)
}
