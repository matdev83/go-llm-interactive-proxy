package lipruntime_test

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// facadeFakeHost is a public-boundary host double for Runtime facade tests.
type facadeFakeHost struct {
	view          lipsdk.ExecutorView
	ready         bool
	traffic       bool
	usage         bool
	evidence      bool
	metering      bool
	rater         bool
	querier       metering.Querier
	refreshErr    error
	refreshCalls  atomic.Int32
	closeErrs     []error
	closeCalls    atomic.Int32
	reload        func(context.Context, sdkreload.Trigger) sdkreload.Result
	status        sdkreload.Status
	snapshotID    int64
	snapshotUsage string
	execID        int64
	execVersion   string
	execState     controlplane.CapabilityState
	execEvidence  string
	readiness     controlplane.ReadinessReportReader
}

func (f *facadeFakeHost) ExecutorView() lipsdk.ExecutorView { return f.view }
func (f *facadeFakeHost) Ready() bool                       { return f.ready }
func (f *facadeFakeHost) Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result {
	if f.reload != nil {
		return f.reload(ctx, trigger)
	}
	return sdkreload.Result{Category: sdkreload.ResultNoop, ActiveGeneration: 1}
}
func (f *facadeFakeHost) Status() sdkreload.Status           { return f.status }
func (f *facadeFakeHost) HasTrafficObservers() bool          { return f.traffic }
func (f *facadeFakeHost) HasUsageObservers() bool            { return f.usage }
func (f *facadeFakeHost) HasProductionEvidenceSink() bool    { return f.evidence }
func (f *facadeFakeHost) HasProductionMetering() bool        { return f.metering }
func (f *facadeFakeHost) HasProductionRater() bool           { return f.rater }
func (f *facadeFakeHost) MeteringQuerier() metering.Querier  { return f.querier }
func (f *facadeFakeHost) HasProductionMeteringQuerier() bool { return f.querier != nil }
func (f *facadeFakeHost) ReadinessReport() controlplane.ReadinessReportReader {
	return f.readiness
}
func (f *facadeFakeHost) SnapshotGenerationID() int64         { return f.snapshotID }
func (f *facadeFakeHost) SnapshotUsageVersion() string        { return f.snapshotUsage }
func (f *facadeFakeHost) ExecutableGenerationID() int64       { return f.execID }
func (f *facadeFakeHost) ExecutableGenerationVersion() string { return f.execVersion }
func (f *facadeFakeHost) ExecutableGenerationState() controlplane.CapabilityState {
	if f.execState == "" {
		return controlplane.CapabilityDisabled
	}
	return f.execState
}
func (f *facadeFakeHost) ExecutableEvidenceObjectID() string { return f.execEvidence }
func (f *facadeFakeHost) RefreshSnapshots(ctx context.Context) error {
	f.refreshCalls.Add(1)
	if ctx == nil {
		return errors.New("nil context")
	}
	return f.refreshErr
}
func (f *facadeFakeHost) Close(context.Context) error {
	n := int(f.closeCalls.Add(1))
	if n-1 < len(f.closeErrs) {
		return f.closeErrs[n-1]
	}
	return nil
}
