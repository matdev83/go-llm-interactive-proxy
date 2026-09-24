package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Historical migration compatibility for Task 17.1 (Migration Strategy steps
// 1-3). This is a read-only compatibility preflight: baseline V1 records keep
// their exact byte/hash/identity semantics, legacy pricing semantics stay
// versioned, and old in-flight V1 work is reported under explicit V1 writer
// ownership. No V2-native E/Q/P/S/R breakdown or local/provider source
// separation is synthesized for V1; unavailable detail stays explicitly
// legacy/opaque. Durable cutover/posting enforcement belongs to Phase 17.3.
const (
	// HistoricalV1WriterVersion owns old in-flight V1 calls. EvidenceVersion
	// zero with no V2 observations/refs/conflicts/dispositions and no
	// auxiliary economic provenance selects it.
	HistoricalV1WriterVersion = "v1"
	// V2WriterVersion owns new source-separated evidence (EvidenceVersion 2
	// with the V1 compatibility projection label).
	V2WriterVersion = "v2"
)

var (
	// ErrHistoricalV1Ambiguous identifies malformed or ambiguous
	// version/identity data that must fail closed.
	ErrHistoricalV1Ambiguous = errors.New("billing: ambiguous historical version")
	// ErrHistoricalV1WriterConflict identifies a writer mismatch against the
	// resolved owner. This read-only preflight reports ownership; durable
	// epoch/posting enforcement belongs to Phase 17.3.
	ErrHistoricalV1WriterConflict = errors.New("billing: historical writer ownership conflict")
)

// HistoricalV1LegView is the legacy-opaque projection of one baseline V1
// call-leg record. It preserves the exact durable key, semantic fingerprint
// and payload hash and carries the scalar FinalBillingEvidence verbatim. It
// never carries V2 observations, E/Q/P/S/R breakdown, or source separation.
type HistoricalV1LegView struct {
	Key                       string
	Fingerprint               string
	PayloadSHA256             string
	CallID                    BillingCallID
	BLegID                    string
	Evidence                  FinalBillingEvidence
	LegacySemantics           string
	WriterVersion             string
	BreakdownAvailable        bool
	SourceSeparationAvailable bool
}

// HistoricalV1CallView is the legacy-opaque projection of one baseline V1
// call record. It preserves key/fingerprint/payload hash and explicit writer
// version without inventing component detail.
type HistoricalV1CallView struct {
	Key             string
	Fingerprint     string
	PayloadSHA256   string
	CallID          BillingCallID
	AccountID       string
	ALegID          string
	SessionID       string
	LegacySemantics string
	WriterVersion   string
}

// HistoricalV1CallBundle groups one V1 call with its V1 legs under a single
// explicit writer version. Mixed-version bundles are rejected as ambiguous.
type HistoricalV1CallBundle struct {
	Call          HistoricalV1CallView
	Legs          []HistoricalV1LegView
	WriterVersion string
}

// Validate fails closed on any malformed or ambiguous V1 leg view.
func (v HistoricalV1LegView) Validate() error {
	if strings.TrimSpace(v.Key) == "" || strings.TrimSpace(v.Fingerprint) == "" {
		return fmt.Errorf("%w: %w: historical V1 leg identity is required", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if len(v.PayloadSHA256) != 64 {
		return fmt.Errorf("%w: %w: historical V1 leg payload hash is required", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if _, err := hex.DecodeString(v.PayloadSHA256); err != nil {
		return fmt.Errorf("%w: %w: historical V1 leg payload hash is malformed", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if err := v.CallID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrHistoricalV1Ambiguous, err)
	}
	if strings.TrimSpace(v.BLegID) == "" || strings.Contains(v.BLegID, ":") {
		return fmt.Errorf("%w: %w: historical V1 B-leg identity is invalid", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	wantKey, err := CallLegUsageKey(v.CallID, v.BLegID)
	if err != nil || wantKey != v.Key {
		return fmt.Errorf("%w: %w: historical V1 leg key mismatch", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if v.WriterVersion != HistoricalV1WriterVersion {
		return fmt.Errorf("%w: historical V1 leg writer must be %q", ErrHistoricalV1Ambiguous, HistoricalV1WriterVersion)
	}
	if v.LegacySemantics != LegacyScalarSemantics {
		return fmt.Errorf("%w: historical V1 leg must carry legacy semantics %q", ErrHistoricalV1Ambiguous, LegacyScalarSemantics)
	}
	if v.BreakdownAvailable || v.SourceSeparationAvailable {
		return fmt.Errorf("%w: historical V1 leg must not claim breakdown or source separation", ErrHistoricalV1Ambiguous)
	}
	return nil
}

// Validate fails closed on any malformed V1 call view.
func (v HistoricalV1CallView) Validate() error {
	if strings.TrimSpace(v.Key) == "" || strings.TrimSpace(v.Fingerprint) == "" {
		return fmt.Errorf("%w: %w: historical V1 call identity is required", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if len(v.PayloadSHA256) != 64 {
		return fmt.Errorf("%w: %w: historical V1 call payload hash is required", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if _, err := hex.DecodeString(v.PayloadSHA256); err != nil {
		return fmt.Errorf("%w: %w: historical V1 call payload hash is malformed", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if err := v.CallID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrHistoricalV1Ambiguous, err)
	}
	wantKey, err := CallUsageKey(v.CallID)
	if err != nil || wantKey != v.Key {
		return fmt.Errorf("%w: %w: historical V1 call key mismatch", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if strings.TrimSpace(v.AccountID) == "" || strings.TrimSpace(v.ALegID) == "" {
		return fmt.Errorf("%w: %w: historical V1 call scope is required", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if v.WriterVersion != HistoricalV1WriterVersion {
		return fmt.Errorf("%w: historical V1 call writer must be %q", ErrHistoricalV1Ambiguous, HistoricalV1WriterVersion)
	}
	if v.LegacySemantics != LegacyScalarSemantics {
		return fmt.Errorf("%w: historical V1 call must carry legacy semantics %q", ErrHistoricalV1Ambiguous, LegacyScalarSemantics)
	}
	return nil
}

// Validate fails closed on mixed-version or malformed bundles.
func (b HistoricalV1CallBundle) Validate() error {
	if err := b.Call.Validate(); err != nil {
		return err
	}
	if len(b.Legs) == 0 {
		return fmt.Errorf("%w: %w: historical V1 bundle has no legs", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if b.WriterVersion != HistoricalV1WriterVersion {
		return fmt.Errorf("%w: historical V1 bundle writer must be %q", ErrHistoricalV1Ambiguous, HistoricalV1WriterVersion)
	}
	for i := range b.Legs {
		if err := b.Legs[i].Validate(); err != nil {
			return err
		}
		if b.Legs[i].CallID != b.Call.CallID {
			return fmt.Errorf("%w: %w: historical V1 bundle leg call mismatch", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
		}
		if b.Legs[i].WriterVersion != b.WriterVersion {
			return fmt.Errorf("%w: historical V1 bundle has mixed writer versions", ErrHistoricalV1Ambiguous)
		}
	}
	return nil
}

// WriterVersionForLeg resolves the explicit writer version for one durable
// call-leg record. Only the existing supported evidence conventions are
// accepted: EvidenceVersion 0 with no V2 envelope and no auxiliary economic
// provenance selects the V1 writer; EvidenceVersion 2 with the V1
// compatibility projection label selects the V2 writer. Unknown versions
// (including 1, 3, 999), contradictory projections, and incompatible
// auxiliary provenance fail closed as ambiguous, aligned with
// CallLegUsageRecord validation rather than future-format guessing.
func WriterVersionForLeg(leg CallLegUsageRecord) (string, error) {
	if leg.EconomicEvidenceVersion != 0 && leg.EconomicEvidenceVersion != EconomicEvidenceDispositionVersionV1 {
		return "", fmt.Errorf("%w: %w: unsupported economic evidence version %d", ErrHistoricalV1Ambiguous, ErrInvalidRecord, leg.EconomicEvidenceVersion)
	}
	if leg.EconomicEvidenceVersion == 0 && len(leg.EconomicDispositions) != 0 {
		return "", fmt.Errorf("%w: %w: economic dispositions require version %d", ErrHistoricalV1Ambiguous, ErrInvalidRecord, EconomicEvidenceDispositionVersionV1)
	}
	if leg.EconomicEvidenceVersion == EconomicEvidenceDispositionVersionV1 && len(leg.EconomicDispositions) == 0 {
		return "", fmt.Errorf("%w: %w: economic evidence version requires dispositions", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	hasV2Payload := len(leg.Observations) != 0 || len(leg.ObservationRefs) != 0 ||
		len(leg.EvidenceConflicts) != 0 || len(leg.EconomicDispositions) != 0
	switch {
	case leg.EvidenceVersion == 0 && !hasV2Payload && leg.EvidenceProjection == "" && leg.EconomicEvidenceVersion == 0:
		return HistoricalV1WriterVersion, nil
	case leg.EvidenceVersion == EvidenceFormatVersionV2 && leg.EvidenceProjection == EvidenceProjectionV1:
		return V2WriterVersion, nil
	default:
		return "", fmt.Errorf("%w: %w: unsupported evidence format version %d", ErrHistoricalV1Ambiguous, ErrInvalidRecord, leg.EvidenceVersion)
	}
}

// ProjectHistoricalV1Leg preserves one sealed baseline V1 leg byte-for-byte:
// it verifies durable replay identity (key/fingerprint) and returns a
// legacy-opaque view with explicit writer version and legacy pricing
// semantics. V2-native observations, breakdown, or source separation are never
// synthesized; a V2 or ambiguous record fails closed.
func ProjectHistoricalV1Leg(leg CallLegUsageRecord) (HistoricalV1LegView, error) {
	sealed, err := leg.Seal()
	if err != nil {
		return HistoricalV1LegView{}, err
	}
	// Seal normalizes the envelope; a caller-supplied record carrying stale
	// key/fingerprint material must not be silently re-keyed. Require exact
	// replay identity when the caller already claims sealed identity.
	if strings.TrimSpace(leg.Key) != "" && leg.Key != sealed.Key {
		return HistoricalV1LegView{}, fmt.Errorf("%w: %w: historical V1 leg key mismatch", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if strings.TrimSpace(leg.Fingerprint) != "" && leg.Fingerprint != sealed.Fingerprint {
		return HistoricalV1LegView{}, fmt.Errorf("%w: %w: historical V1 leg fingerprint mismatch", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	version, err := WriterVersionForLeg(sealed)
	if err != nil {
		return HistoricalV1LegView{}, err
	}
	if version != HistoricalV1WriterVersion {
		return HistoricalV1LegView{}, fmt.Errorf("%w: record is owned by %q, not historical V1", ErrHistoricalV1WriterConflict, version)
	}
	raw, err := json.Marshal(sealed)
	if err != nil {
		return HistoricalV1LegView{}, fmt.Errorf("%w: historical V1 leg encode: %v", ErrInvalidRecord, err)
	}
	sum := sha256.Sum256(raw)
	view := HistoricalV1LegView{
		Key:                       sealed.Key,
		Fingerprint:               sealed.Fingerprint,
		PayloadSHA256:             hex.EncodeToString(sum[:]),
		CallID:                    sealed.CallID,
		BLegID:                    sealed.BLegID,
		Evidence:                  sealed.Evidence,
		LegacySemantics:           LegacyScalarSemantics,
		WriterVersion:             HistoricalV1WriterVersion,
		BreakdownAvailable:        false,
		SourceSeparationAvailable: false,
	}
	if err := view.Validate(); err != nil {
		return HistoricalV1LegView{}, err
	}
	return view, nil
}

// ProjectHistoricalV1Call preserves one sealed baseline V1 call record with
// explicit V1 writer ownership and legacy pricing semantics. The call schema
// version alone does not prove writer provenance; callers needing ownership
// must also resolve the call's legs (as ReadHistoricalV1CallBundle does).
func ProjectHistoricalV1Call(call CallUsageRecord) (HistoricalV1CallView, error) {
	sealed, err := call.Seal()
	if err != nil {
		return HistoricalV1CallView{}, err
	}
	if strings.TrimSpace(call.Key) != "" && call.Key != sealed.Key {
		return HistoricalV1CallView{}, fmt.Errorf("%w: %w: historical V1 call key mismatch", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if strings.TrimSpace(call.Fingerprint) != "" && call.Fingerprint != sealed.Fingerprint {
		return HistoricalV1CallView{}, fmt.Errorf("%w: %w: historical V1 call fingerprint mismatch", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if sealed.SchemaVersion != CurrentRecordSchemaVersion {
		return HistoricalV1CallView{}, fmt.Errorf("%w: %w: unsupported call schema version %d", ErrHistoricalV1Ambiguous, ErrInvalidRecord, sealed.SchemaVersion)
	}
	raw, err := json.Marshal(sealed)
	if err != nil {
		return HistoricalV1CallView{}, fmt.Errorf("%w: historical V1 call encode: %v", ErrInvalidRecord, err)
	}
	sum := sha256.Sum256(raw)
	view := HistoricalV1CallView{
		Key:             sealed.Key,
		Fingerprint:     sealed.Fingerprint,
		PayloadSHA256:   hex.EncodeToString(sum[:]),
		CallID:          sealed.CallID,
		AccountID:       sealed.AccountID,
		ALegID:          sealed.ALegID,
		SessionID:       sealed.SessionID,
		LegacySemantics: LegacyScalarSemantics,
		WriterVersion:   HistoricalV1WriterVersion,
	}
	if err := view.Validate(); err != nil {
		return HistoricalV1CallView{}, err
	}
	return view, nil
}

// validateHistoricalLegClaimIdentity requires caller-supplied sealed
// identity and rejects silent normalization. Empty keys/fingerprints fail
// closed, and any claimed Key/Fingerprint must match the sealed recomputation
// exactly. Seal errors propagate unchanged.
func validateHistoricalLegClaimIdentity(leg CallLegUsageRecord, sealed CallLegUsageRecord) error {
	if strings.TrimSpace(leg.Key) == "" || strings.TrimSpace(leg.Fingerprint) == "" {
		return fmt.Errorf("%w: %w: historical writer claim requires sealed key and fingerprint", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if leg.Key != sealed.Key {
		return fmt.Errorf("%w: %w: historical writer claim key mismatch", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if leg.Fingerprint != sealed.Fingerprint {
		return fmt.Errorf("%w: %w: historical writer claim fingerprint mismatch", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	return nil
}

// CheckHistoricalV1WriterClaim is a read-only ownership preflight for old
// in-flight work. Every leg must carry valid sealed identity matching its
// sealed recomputation, resolve to one unambiguous writer version, and belong
// to a single call; a claimant other than that owner fails with
// ErrHistoricalV1WriterConflict. Empty, mixed-version, mixed-call, or
// unknown-claimant inputs fail closed. Durable epoch/posting enforcement
// belongs to Phase 17.3; this helper performs no writes.
func CheckHistoricalV1WriterClaim(legs []CallLegUsageRecord, claimant string) error {
	if len(legs) == 0 {
		return fmt.Errorf("%w: %w: historical writer claim requires legs", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
	}
	if claimant != HistoricalV1WriterVersion && claimant != V2WriterVersion {
		return fmt.Errorf("%w: %w: unknown writer claimant %q", ErrHistoricalV1Ambiguous, ErrInvalidRecord, claimant)
	}
	owner := ""
	var ownerCall BillingCallID
	first := true
	for i := range legs {
		// Validate the original durable envelope before any Seal
		// normalization. Seal restores a missing V2 projection label, so
		// resolving only the sealed record would repair a malformed
		// version envelope instead of rejecting it.
		versionOriginal, err := WriterVersionForLeg(legs[i])
		if err != nil {
			return err
		}
		sealed, err := legs[i].Seal()
		if err != nil {
			return err
		}
		if err := validateHistoricalLegClaimIdentity(legs[i], sealed); err != nil {
			return err
		}
		version, err := WriterVersionForLeg(sealed)
		if err != nil {
			return err
		}
		if version != versionOriginal {
			return fmt.Errorf("%w: historical writer claim version changed under normalization", ErrHistoricalV1Ambiguous)
		}
		if first {
			owner = version
			ownerCall = sealed.CallID
			first = false
		} else {
			if owner != version {
				return fmt.Errorf("%w: mixed historical writer versions %q and %q", ErrHistoricalV1Ambiguous, owner, version)
			}
			if sealed.CallID != ownerCall {
				return fmt.Errorf("%w: %w: historical writer claim spans multiple calls", ErrHistoricalV1Ambiguous, ErrInvalidRecord)
			}
		}
	}
	if claimant != owner {
		return fmt.Errorf("%w: call is owned by %q, claimant is %q", ErrHistoricalV1WriterConflict, owner, claimant)
	}
	return nil
}
