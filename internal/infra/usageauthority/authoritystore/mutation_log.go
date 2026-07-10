package authoritystore

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"

// MutationLog captures per-call mutations emitted by storeCore so adapters
// (memory: discard; durable: persist) can apply them without re-serializing
// the entire in-memory state.
//
// Contracts:
//   - Empty logs (no captures) mean no flush is required; the durable adapter
//     can safely return without a transaction.
//   - For the same row mutated multiple times in a single call, the LAST
//     captured state is what the durable adapter should persist.
//   - Implementations must be safe to call from in-memory mutation sites
//     without external synchronization (the caller holds the store mutex).
type MutationLog interface {
	CaptureLimitUpdate(rowKey string, row *controlplane.AccountingLimitStatusRow)
	CaptureReservationUpsert(reservationKey string, rec *reservationRecord)
	CaptureDecisionAppend(rec decisionRecord)
}

// discardMutationLog is the no-op log used by the in-memory store.
type discardMutationLog struct{}

func (discardMutationLog) CaptureLimitUpdate(string, *controlplane.AccountingLimitStatusRow) {
}
func (discardMutationLog) CaptureReservationUpsert(string, *reservationRecord) {
}
func (discardMutationLog) CaptureDecisionAppend(decisionRecord) {
}

// recordingMutationLog accumulates mutations for the durable adapter to apply
// during flush. Maps coalesce repeated updates within a single call: the LAST
// captured state wins.
type recordingMutationLog struct {
	limitUpdates       map[string]*controlplane.AccountingLimitStatusRow
	reservationUpserts map[string]*reservationRecord
	decisionsAppended  []decisionRecord
}

func newRecordingMutationLog() *recordingMutationLog {
	return &recordingMutationLog{
		limitUpdates:       make(map[string]*controlplane.AccountingLimitStatusRow),
		reservationUpserts: make(map[string]*reservationRecord),
	}
}

func (r *recordingMutationLog) CaptureLimitUpdate(rowKey string, row *controlplane.AccountingLimitStatusRow) {
	if r == nil || row == nil {
		return
	}
	cp := *row
	r.limitUpdates[rowKey] = &cp
}

func (r *recordingMutationLog) CaptureReservationUpsert(reservationKey string, rec *reservationRecord) {
	if r == nil || rec == nil {
		return
	}
	cp := *rec
	r.reservationUpserts[reservationKey] = &cp
}

func (r *recordingMutationLog) CaptureDecisionAppend(rec decisionRecord) {
	if r == nil {
		return
	}
	r.decisionsAppended = append(r.decisionsAppended, rec)
}

// isEmpty reports whether the log captured no mutations during the call.
func (r *recordingMutationLog) isEmpty() bool {
	return r == nil ||
		(len(r.limitUpdates) == 0 && len(r.reservationUpserts) == 0 && len(r.decisionsAppended) == 0)
}
