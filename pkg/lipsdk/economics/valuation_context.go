package economics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// LegacyValuationContextHash is the explicit marker used by historical
// durable valuation rows that predate the context-hash column. It is kept
// empty so those rows remain readable without being mistaken for a computed
// context.
const LegacyValuationContextHash = ""

type valuationContextContent struct {
	ContentRef  string `json:"content_ref"`
	ContentHash string `json:"content_hash"`
}

type valuationContextSnapshot struct {
	ID       string                  `json:"id"`
	Version  string                  `json:"version"`
	RaterID  string                  `json:"rater_id"`
	PolicyID string                  `json:"policy_id"`
	Content  valuationContextContent `json:"content"`
}

type valuationContextPreimage struct {
	Version              uint32                       `json:"version"`
	Perspective          metering.EconomicPerspective `json:"perspective"`
	Basis                ValuationBasis               `json:"basis"`
	Subject              metering.SubjectRef          `json:"subject"`
	Scope                string                       `json:"scope"`
	Payer                metering.PaymentParty        `json:"payer"`
	Rater                valuationContextSnapshot     `json:"rater"`
	Tariff               valuationContextSnapshot     `json:"tariff"`
	Policy               valuationContextSnapshot     `json:"policy"`
	QualifierSnapshot    string                       `json:"qualifier_snapshot"`
	QualifierSnapshotRef valuationContextContent      `json:"qualifier_snapshot_ref"`
	EffectiveQualifiers  []metering.Dimension         `json:"effective_qualifiers,omitempty"`
}

func valuationContextContentFor(ref *SnapshotContentRef) valuationContextContent {
	if ref == nil {
		return valuationContextContent{}
	}
	return valuationContextContent{ContentRef: ref.ContentRef, ContentHash: ref.ContentHash}
}

func valuationContextSnapshotFor(ref RatingSnapshotRef, content *SnapshotContentRef) valuationContextSnapshot {
	return valuationContextSnapshot{
		ID: ref.ID, Version: ref.Version, RaterID: ref.RaterID,
		Content: valuationContextContentFor(content),
	}
}

func valuationContextPolicyFor(ref PolicySnapshotRef, content *SnapshotContentRef) valuationContextSnapshot {
	return valuationContextSnapshot{
		ID: ref.ID, Version: ref.Version, PolicyID: ref.PolicyID,
		Content: valuationContextContentFor(content),
	}
}

// CanonicalContextJSON returns the deterministic economic-context preimage for
// a valuation. It includes trusted subject/scope/perspective/payer identity,
// all snapshot VersionRef IDs and versions, provider/policy IDs, and every
// content resolver reference/hash. Absent content uses an explicit empty
// object so legacy and current callers share one unambiguous representation.
// Publication timestamps are excluded because they describe retrieval
// metadata rather than the economic interpretation.
func (v Valuation) CanonicalContextJSON() ([]byte, error) {
	qualifiers := append([]metering.Dimension(nil), v.EffectiveQualifiers...)
	if err := validateDimensions(qualifiers, ErrInvalidValuation); err != nil {
		return nil, fmt.Errorf("economics: effective qualifiers: %w", err)
	}
	slices.SortFunc(qualifiers, func(a, b metering.Dimension) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Value, b.Value)
	})
	preimage := valuationContextPreimage{
		Version:              v.Version,
		Perspective:          v.Perspective,
		Basis:                v.Basis,
		Subject:              v.Subject.Clone(),
		Scope:                v.Scope,
		Payer:                v.Payer,
		Rater:                valuationContextSnapshotFor(v.Rater, v.RaterContent),
		Tariff:               valuationContextSnapshotFor(v.Tariff, v.TariffContent),
		Policy:               valuationContextPolicyFor(v.Policy, v.PolicyContent),
		QualifierSnapshot:    v.QualifierSnapshot,
		QualifierSnapshotRef: valuationContextContentFor(v.QualifierSnapshotRef),
		EffectiveQualifiers:  qualifiers,
	}
	return json.Marshal(preimage)
}

// ContextHash returns the lowercase SHA-256 digest of CanonicalContextJSON.
// Invalid JSON serialization is not expected for this bounded DTO; an empty
// result keeps callers from treating an unavailable digest as a valid hash.
func (v Valuation) ContextHash() string {
	payload, err := v.CanonicalContextJSON()
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
