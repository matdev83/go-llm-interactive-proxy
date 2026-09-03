package compactioncompose

import (
	"context"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/resultmerge"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

var ErrInvalidCompactionContinuityResultAdapter = errors.New("compactioncompose: invalid compaction-continuity result adapter")

// CompactionContinuityResultAdapter binds one process-owned coordinator and
// authoritative parent key without decoding a branch binding into a BranchKey.
type CompactionContinuityResultAdapter struct {
	coordinator *compactioncontinuity.BranchCoordinator
	parentKey   compactioncontinuity.BranchKey
	binding     string
}

// NewCompactionContinuityResultAdapter binds result consumption to the process-owned coordinator and authoritative parentKey.
func NewCompactionContinuityResultAdapter(coordinator *compactioncontinuity.BranchCoordinator, parentKey compactioncontinuity.BranchKey) (*CompactionContinuityResultAdapter, error) {
	if coordinator == nil {
		return nil, fmt.Errorf("%w: nil branch coordinator", ErrInvalidCompactionContinuityResultAdapter)
	}
	binding, err := compactioncontinuity.BranchBinding(parentKey)
	if err != nil {
		return nil, fmt.Errorf("%w: parent key: %w", ErrInvalidCompactionContinuityResultAdapter, err)
	}
	return &CompactionContinuityResultAdapter{
		coordinator: coordinator,
		parentKey:   parentKey,
		binding:     binding,
	}, nil
}

// ValidatePendingJob validates the job and translates its defensive state copy.
func (a *CompactionContinuityResultAdapter) ValidatePendingJob(ctx context.Context, branchBinding string, jobID auxiliary.JobID) (resultmerge.ParentState, error) {
	if err := a.validateBinding(branchBinding); err != nil {
		return resultmerge.ParentState{}, err
	}
	state, err := a.coordinator.ValidatePendingJob(ctx, a.parentKey, jobID)
	if err != nil {
		return resultmerge.ParentState{}, err
	}
	return a.parentState(state)
}

// CommitCapsuleForJob delegates the job-bound CAS to the real coordinator
// after validating both branch-binding arguments against the captured parent.
func (a *CompactionContinuityResultAdapter) CommitCapsuleForJob(ctx context.Context, branchBinding string, jobID auxiliary.JobID, resultBranchBinding string, expectedRevision uint64, capsule []byte, digest [32]byte, sourceHighWatermark string) (resultmerge.ParentState, error) {
	if err := a.validateBinding(branchBinding); err != nil {
		return resultmerge.ParentState{}, err
	}
	if err := a.validateBinding(resultBranchBinding); err != nil {
		return resultmerge.ParentState{}, err
	}
	state, err := a.coordinator.CommitCapsuleForJob(ctx, a.parentKey, jobID, resultBranchBinding, expectedRevision, capsule, digest, sourceHighWatermark)
	if err != nil {
		return resultmerge.ParentState{}, err
	}
	return a.parentState(state)
}

func (a *CompactionContinuityResultAdapter) validateBinding(binding string) error {
	if a == nil || a.coordinator == nil || binding != a.binding {
		return compactioncontinuity.ErrBranchMismatch
	}
	return nil
}

func (a *CompactionContinuityResultAdapter) parentState(state compactioncontinuity.BranchState) (resultmerge.ParentState, error) {
	if state.PendingJobBranchBinding != "" && state.PendingJobBranchBinding != a.binding {
		return resultmerge.ParentState{}, compactioncontinuity.ErrBranchMismatch
	}
	if state.PendingJobID != "" && state.PendingJobBranchBinding != a.binding {
		return resultmerge.ParentState{}, compactioncontinuity.ErrBranchMismatch
	}
	return resultmerge.ParentState{
		BranchBinding:            a.binding,
		Revision:                 state.Revision,
		CapsuleJSON:              append([]byte(nil), state.CapsuleJSON...),
		CapsuleDigest:            state.CapsuleDigest,
		SourceHighWatermark:      state.SourceHighWatermark,
		PendingJobID:             state.PendingJobID,
		PendingJobTargetRevision: state.PendingJobTargetRevision,
		PendingJobBranchBinding:  state.PendingJobBranchBinding,
	}, nil
}

var _ resultmerge.ParentCoordinator = (*CompactionContinuityResultAdapter)(nil)
