package planeparity_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/planeparity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/stretchr/testify/require"
)

type harnessHook struct{ id string }

func (h harnessHook) ID() string                      { return h.id }
func (harnessHook) Order() int                        { return 0 }
func (harnessHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (harnessHook) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type harnessRequestPartHook struct{ id string }

func (h harnessRequestPartHook) ID() string                      { return h.id }
func (harnessRequestPartHook) Order() int                        { return 0 }
func (harnessRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (harnessRequestPartHook) HandleRequestParts(context.Context, *lipapi.Call, sdkhooks.PartMeta) error {
	return nil
}

type harnessResponsePartHook struct{ id string }

func (h harnessResponsePartHook) ID() string                      { return h.id }
func (harnessResponsePartHook) Order() int                        { return 0 }
func (harnessResponsePartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (harnessResponsePartHook) HandleEvent(context.Context, *lipapi.Event, sdkhooks.PartMeta) error {
	return nil
}

type harnessLifecycle struct{ id string }

func (harnessLifecycle) Start(context.Context) error { return nil }
func (harnessLifecycle) Stop(context.Context) error  { return nil }

type harnessTerminalProvider struct{ id string }

func (p harnessTerminalProvider) ID() string { return p.id }
func (harnessTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "done"}, nil
}

func TestAssertDualPathParity_Success(t *testing.T) {
	t.Parallel()

	b1 := lipfeature.FeatureBundle{
		SchemaVersion:                    lipfeature.SchemaVersionV1,
		SubmitHooks:                      []sdkhooks.SubmitHook{harnessHook{id: "hook-1"}},
		RequestPartHooks:                 []sdkhooks.RequestPartHook{harnessRequestPartHook{id: "reqpart-1"}},
		ResponsePartHooks:                []sdkhooks.ResponsePartHook{harnessResponsePartHook{id: "resppart-1"}},
		ToolCallFinalizationMaxArgsBytes: 4096,
		Lifecycles:                       []lipplugin.Lifecycle{harnessLifecycle{id: "life-1"}},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion:                    lipfeature.SchemaVersionV1,
		SubmitHooks:                      []sdkhooks.SubmitHook{harnessHook{id: "hook-2"}},
		RequestPartHooks:                 []sdkhooks.RequestPartHook{harnessRequestPartHook{id: "reqpart-2"}},
		ResponsePartHooks:                []sdkhooks.ResponsePartHook{harnessResponsePartHook{id: "resppart-2"}},
		ToolCallFinalizationMaxArgsBytes: 2048,
		TerminalDecisionProvider:         harnessTerminalProvider{id: "term-1"},
		Lifecycles:                       []lipplugin.Lifecycle{harnessLifecycle{id: "life-2"}},
	}

	planeparity.AssertDualPathParity(t, b1, b2)
}

func TestMergeBundlesGenerated_Conflict(t *testing.T) {
	t.Parallel()

	b1 := lipfeature.FeatureBundle{
		SchemaVersion:            lipfeature.SchemaVersionV1,
		TerminalDecisionProvider: harnessTerminalProvider{id: "term-1"},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion:            lipfeature.SchemaVersionV1,
		TerminalDecisionProvider: harnessTerminalProvider{id: "term-2"},
	}

	res, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.Error(t, err)
	require.ErrorIs(t, err, lipfeature.ErrExclusiveConflict)
	require.Equal(t, featurebundle.GeneratedMergeSurface{}, res)
}

func TestMergeBundlesViaGenerated_Equivalence(t *testing.T) {
	t.Parallel()

	b1 := lipfeature.FeatureBundle{
		SchemaVersion:                    lipfeature.SchemaVersionV1,
		SubmitHooks:                      []sdkhooks.SubmitHook{harnessHook{id: "h1"}},
		RequestPartHooks:                 []sdkhooks.RequestPartHook{harnessRequestPartHook{id: "rp1"}},
		ResponsePartHooks:                []sdkhooks.ResponsePartHook{harnessResponsePartHook{id: "rsp1"}},
		ToolCallFinalizationMaxArgsBytes: 8192,
		TerminalDecisionProvider:         harnessTerminalProvider{id: "term-1"},
		Lifecycles:                       []lipplugin.Lifecycle{harnessLifecycle{id: "l1"}},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion:                    lipfeature.SchemaVersionV1,
		SubmitHooks:                      []sdkhooks.SubmitHook{harnessHook{id: "h2"}},
		RequestPartHooks:                 []sdkhooks.RequestPartHook{harnessRequestPartHook{id: "rp2"}},
		ResponsePartHooks:                []sdkhooks.ResponsePartHook{harnessResponsePartHook{id: "rsp2"}},
		ToolCallFinalizationMaxArgsBytes: 4096,
		Lifecycles:                       []lipplugin.Lifecycle{harnessLifecycle{id: "l2"}},
	}

	legacy, errLegacy := featurebundle.MergeBundlesChecked(b1, b2)
	require.NoError(t, errLegacy)

	viaGen, errGen := featurebundle.MergeBundlesViaGenerated(b1, b2)
	require.NoError(t, errGen)

	require.Equal(t, legacy, viaGen)
}
