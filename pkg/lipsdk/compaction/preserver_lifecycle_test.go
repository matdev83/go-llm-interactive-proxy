package compaction_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

type lifecycleContractStub struct{}

func (lifecycleContractStub) ID() string { return "lifecycle" }
func (lifecycleContractStub) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (lifecycleContractStub) RequestOpened(context.Context, lipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (lifecycleContractStub) BeforeResponseRelease(context.Context, *lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (lifecycleContractStub) RequestOpenFailed(context.Context, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (lifecycleContractStub) AfterResponseRelease(context.Context, lipapi.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

var (
	_ compaction.Preserver                     = lifecycleContractStub{}
	_ compaction.RequestOpenFailedPreserver    = lifecycleContractStub{}
	_ compaction.AfterResponseReleasePreserver = lifecycleContractStub{}
)

func TestPreserverOptionalLifecycleInterfacesRemainAdditive(t *testing.T) {
	t.Parallel()
	var _ compaction.Preserver = lifecycleContractStub{}
	var _ compaction.RequestOpenFailedPreserver = lifecycleContractStub{}
	var _ compaction.AfterResponseReleasePreserver = lifecycleContractStub{}
}
