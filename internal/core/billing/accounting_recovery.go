package billing

import (
	"errors"
	"fmt"
	"strings"
)

// Compatible rollback and recovery policy for Task 17.4 (Migration Strategy
// step 7, requirements 10.5, 11.6, 17.5, 18.4).
//
// Capture-only rollback (serving a store with a pre-cutover binary again) is
// permitted only before any V2 monetary posting has become durable and while
// the store compatibility floor remains V1. Once V2 financial postings exist
// — or the durable floor already requires V2 readers — a process must prove
// V2 format plus marker epoch-fencing capability before serving; otherwise it
// fails startup with ErrAccountingStaleBinary or runs only with strict
// monetary admissions deterministically quiesced (ErrAccountingStrictQuiesced
// on every admission attempt). There is no silent downgrade: pending provider
// evidence never authorizes an old monetary writer, and optional raw-transport
// retention loss never affects canonical linkage because canonical hashes
// cover canonical records, never raw upstream responses.
//
// Optional raw-evidence contract: privileged raw-transport/capture bytes
// (traffic.RawCaptureSink, DisabledRawCapture by default) are deliberately
// absent/unsupported in durable accounting. Canonical metering observations
// carry bounded allowlisted SafeEvidenceField lexemes only, never raw
// provider bodies, and recovery identity uses canonical observation refs
// and payload hashes (ObservationRef.PayloadHash over canonical records),
// never a hash of an entire upstream response. Raw retention expiry — or
// the raw sink never having been enabled — therefore never affects
// canonical linkage, balances, journals, pins, or pending-work recovery;
// no raw retention subsystem is introduced or required here.
//
// All decisions are pure functions of the durable per-store snapshot plus the
// binary capability: no globals, no SQL, no provider SDKs. The store layer
// fills the snapshot; the runtime composition enforces the verdict at
// startup.

var (
	// ErrAccountingRollbackBlocked identifies a capture-rollback request
	// against a store that already carries durable V2 financial state.
	ErrAccountingRollbackBlocked = errors.New("billing: accounting capture rollback blocked after V2 monetary posting")
	// ErrAccountingStaleBinary identifies a serving binary whose accounting
	// reader/epoch capability cannot operate the durable financial state.
	ErrAccountingStaleBinary = errors.New("billing: accounting binary is not compatible with durable financial state")
	// ErrAccountingStrictQuiesced identifies a denied strict monetary
	// admission while the process runs quiesced pending compatible recovery.
	ErrAccountingStrictQuiesced = errors.New("billing: strict monetary admissions quiesced pending compatible recovery")
)

// AccountingBinaryCapability declares the accounting format/epoch-reader
// capability one serving binary proves at startup. SupportsV1Reader covers
// the historical V1 writer lineage; SupportsV2Reader covers V2 source-
// separated formats; EpochAware covers marker version/epoch claim fencing
// (a V2 reader without epoch fencing could double-post across an epoch
// change, so it is not a compatible V2 binary).
type AccountingBinaryCapability struct {
	SupportsV1Reader bool
	SupportsV2Reader bool
	EpochAware       bool
}

// Validate fails closed unless the capability can serve at least one
// accounting lineage.
func (c AccountingBinaryCapability) Validate() error {
	if !c.SupportsV1Reader && !c.SupportsV2Reader {
		return fmt.Errorf("%w: %w: binary supports no accounting reader", ErrAccountingStaleBinary, ErrInvalidRecord)
	}
	return nil
}

// CurrentAccountingBinaryCapability returns the capability of this binary:
// both reader lineages plus epoch fencing.
func CurrentAccountingBinaryCapability() AccountingBinaryCapability {
	return AccountingBinaryCapability{SupportsV1Reader: true, SupportsV2Reader: true, EpochAware: true}
}

// SupportsFloor reports whether the capability may operate a store whose
// durable compatibility floor is floor. Unknown future floors are never
// supported: a binary must not silently serve financial state it cannot
// parse.
func (c AccountingBinaryCapability) SupportsFloor(floor string) bool {
	switch floor {
	case HistoricalV1WriterVersion:
		return c.SupportsV1Reader
	case V2WriterVersion:
		return c.SupportsV2Reader && c.EpochAware
	default:
		return false
	}
}

// AccountingRecoverySnapshot is the durable per-store recovery input for one
// deployment/store boundary. MarkerFound is false for legacy stores that
// predate the cutover marker; those stores converge on the explicit V1 floor.
// HasV2MonetaryPosting reports durable V2 financial authority (V2-owned
// posting pins or V2-owned monetary work), never shadow capture or pending
// evidence-only work.
type AccountingRecoverySnapshot struct {
	StoreID              string
	MarkerFound          bool
	Marker               AccountingCutoverMarker
	HasV2MonetaryPosting bool
}

// Validate fails closed on malformed snapshots. A present marker must be
// internally consistent; the store scope is always required.
func (s AccountingRecoverySnapshot) Validate() error {
	if strings.TrimSpace(s.StoreID) == "" {
		return fmt.Errorf("%w: %w: recovery store scope is required", ErrAccountingStaleBinary, ErrInvalidRecord)
	}
	if strings.TrimSpace(s.StoreID) != s.StoreID {
		return fmt.Errorf("%w: %w: recovery store scope must be trimmed", ErrAccountingStaleBinary, ErrInvalidRecord)
	}
	if !s.MarkerFound {
		return nil
	}
	if s.Marker.StoreID != s.StoreID {
		return fmt.Errorf("%w: %w: recovery marker store mismatch", ErrAccountingStaleBinary, ErrInvalidRecord)
	}
	if err := s.Marker.Validate(); err != nil {
		return err
	}
	return nil
}

// EffectiveFloor returns the compatibility floor governing the store: the
// durable marker floor when present, otherwise the legacy V1 default.
func (s AccountingRecoverySnapshot) EffectiveFloor() string {
	if !s.MarkerFound {
		return HistoricalV1WriterVersion
	}
	return s.Marker.CompatibilityFloor
}

// CheckCaptureRollbackAllowed permits capture-only rollback (serving the
// store with a pre-cutover binary again) only while no V2 monetary posting
// is durable and the compatibility floor remains V1. Any V2 posting, a V2
// floor, or an unknown floor blocks rollback: an old writer must never
// reopen against unsupported new-format financial state.
func CheckCaptureRollbackAllowed(snapshot AccountingRecoverySnapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if snapshot.HasV2MonetaryPosting {
		return fmt.Errorf("%w: store %q carries durable V2 monetary postings",
			ErrAccountingRollbackBlocked, snapshot.StoreID)
	}
	if floor := snapshot.EffectiveFloor(); floor != HistoricalV1WriterVersion {
		return fmt.Errorf("%w: store %q requires %q readers",
			ErrAccountingRollbackBlocked, snapshot.StoreID, floor)
	}
	return nil
}

// AccountingRequiresStrictQuiesce is the single deterministic predicate
// behind both startup rejection and explicit quiesce: it is true exactly
// when the binary cannot safely serve the durable financial state. Invalid
// snapshots or capabilities quiesce (fail closed).
func AccountingRequiresStrictQuiesce(snapshot AccountingRecoverySnapshot, capability AccountingBinaryCapability) bool {
	if err := snapshot.Validate(); err != nil {
		return true
	}
	if err := capability.Validate(); err != nil {
		return true
	}
	floor := snapshot.EffectiveFloor()
	if snapshot.HasV2MonetaryPosting || floor == V2WriterVersion {
		if !capability.SupportsV2Reader || !capability.EpochAware {
			return true
		}
	}
	return !capability.SupportsFloor(floor)
}

// CheckAccountingStartup proves the binary may serve the store before it
// admits or posts anything. Compatible binaries pass; anything requiring
// quiesce fails with ErrAccountingStaleBinary so the process never serves
// (and never silently downgrades) unsupported financial state.
func CheckAccountingStartup(snapshot AccountingRecoverySnapshot, capability AccountingBinaryCapability) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if err := capability.Validate(); err != nil {
		return err
	}
	if AccountingRequiresStrictQuiesce(snapshot, capability) {
		return fmt.Errorf("%w: store %q floor %q postings=%v capability=%+v",
			ErrAccountingStaleBinary, snapshot.StoreID, snapshot.EffectiveFloor(),
			snapshot.HasV2MonetaryPosting, capability)
	}
	return nil
}
