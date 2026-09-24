package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

var (
	// ErrSubmissionFeeInvalid means a customer valuation carries a submission
	// fee that is not safe to admit to the durable settlement boundary.
	ErrSubmissionFeeInvalid = errors.New("billing: invalid submission fee")
	// ErrSubmissionFeeIdentityMissing means a submission-scoped fee cannot be
	// keyed because the trusted call identity did not carry SubmissionID.
	ErrSubmissionFeeIdentityMissing = errors.New("billing: submission fee identity missing")
)

// SubmissionFeeClaim is the immutable commercial context needed by durable
// settlement to charge one submission fee while retaining every call fee.
// SubmissionID is trusted from CallUsageRecord; it is never read from a
// client-provided valuation subject.
type SubmissionFeeClaim struct {
	SubmissionID string
	Amount       Money

	ContextFingerprint string
	TariffID           string
	TariffVersion      string
	TariffContentRef   string
	TariffContentHash  string
	PolicyID           string
	PolicyVersion      string
	PolicyContentRef   string
	PolicyContentHash  string
}

func (c SubmissionFeeClaim) Validate() error {
	if c.SubmissionID == "" || c.ContextFingerprint == "" {
		return fmt.Errorf("%w: submission and context identities are required", ErrSubmissionFeeInvalid)
	}
	if err := c.Amount.Validate(); err != nil {
		return fmt.Errorf("%w: amount: %v", ErrSubmissionFeeInvalid, err)
	}
	if c.Amount.Nano < 0 {
		return fmt.Errorf("%w: amount cannot be negative", ErrSubmissionFeeInvalid)
	}
	for name, value := range map[string]string{
		"tariff id": c.TariffID, "tariff version": c.TariffVersion,
		"policy id": c.PolicyID, "policy version": c.PolicyVersion,
	} {
		if value == "" {
			return fmt.Errorf("%w: %s is required", ErrSubmissionFeeInvalid, name)
		}
	}
	return nil
}

// SubmissionFeeClaimForValuation extracts one durable claim from a complete
// customer valuation. A valuation without a submission fixed-fee line is not
// a claim, which preserves legacy/per-call settlement behavior.
func SubmissionFeeClaimForValuation(call CallUsageRecord, valuation economics.Valuation) (SubmissionFeeClaim, bool, error) {
	var hasSubmissionFee bool
	for _, line := range valuation.Lines {
		if line.FixedFee != nil && line.FixedFee.Scope == economics.FixedFeeScopeSubmission {
			hasSubmissionFee = true
			break
		}
	}
	if !hasSubmissionFee {
		return SubmissionFeeClaim{}, false, nil
	}
	if call.SubmissionID == "" {
		return SubmissionFeeClaim{}, false, ErrSubmissionFeeIdentityMissing
	}
	canonical, err := valuation.Canonical()
	if err != nil {
		return SubmissionFeeClaim{}, false, fmt.Errorf("%w: valuation: %v", ErrSubmissionFeeInvalid, err)
	}
	if canonical.Completeness != economics.CompletenessComplete {
		return SubmissionFeeClaim{}, false, fmt.Errorf("%w: valuation is %s", ErrSubmissionFeeInvalid, canonical.Completeness)
	}
	var total economics.Money
	var lines []submissionFeeLineIdentity
	for _, line := range canonical.Lines {
		if line.FixedFee == nil || line.FixedFee.Scope != economics.FixedFeeScopeSubmission {
			continue
		}
		if line.Status != economics.RatingLineRated && line.Status != economics.RatingLineExplicitFree {
			return SubmissionFeeClaim{}, false, fmt.Errorf("%w: line %q is %s", ErrSubmissionFeeInvalid, line.ID, line.Status)
		}
		if line.RoundedAmount == nil || !line.RoundedAmount.Present {
			return SubmissionFeeClaim{}, false, fmt.Errorf("%w: line %q has no rounded amount", ErrSubmissionFeeInvalid, line.ID)
		}
		amount := *line.RoundedAmount
		if err := amount.Validate(); err != nil {
			return SubmissionFeeClaim{}, false, fmt.Errorf("%w: line %q amount: %v", ErrSubmissionFeeInvalid, line.ID, err)
		}
		if amount.NanoUnits < 0 {
			return SubmissionFeeClaim{}, false, fmt.Errorf("%w: line %q amount cannot be negative", ErrSubmissionFeeInvalid, line.ID)
		}
		if total.Present {
			total, err = total.Add(amount)
		} else {
			total = amount
		}
		if err != nil {
			return SubmissionFeeClaim{}, false, fmt.Errorf("%w: total: %v", ErrSubmissionFeeInvalid, err)
		}
		lines = append(lines, submissionFeeLineIdentity{
			ID: line.ID, RuleID: line.RuleID, ItemID: line.ItemID, FixedFee: *line.FixedFee,
			Amount: line.Amount, AmountNumerator: line.AmountNumerator, AmountDenominator: line.AmountDenominator,
			RoundedAmount: amount, RoundingScope: line.RoundingScope, RoundingPolicy: line.RoundingPolicy,
			IncludedUnit: line.IncludedUnit, Status: line.Status, ChargeKind: line.ChargeKind,
		})
	}
	if len(lines) == 0 || !total.Present {
		return SubmissionFeeClaim{}, false, fmt.Errorf("%w: submission fee lines are empty", ErrSubmissionFeeInvalid)
	}
	contextFingerprint, err := submissionFeeContextFingerprint(canonical, lines)
	if err != nil {
		return SubmissionFeeClaim{}, false, err
	}
	claim := SubmissionFeeClaim{
		SubmissionID:       call.SubmissionID,
		Amount:             Money{Nano: total.NanoUnits, Currency: total.Currency},
		ContextFingerprint: contextFingerprint,
		TariffID:           canonical.Tariff.ID,
		TariffVersion:      canonical.Tariff.Version,
		PolicyID:           canonical.Policy.PolicyID,
		PolicyVersion:      canonical.Policy.Version,
	}
	if claim.PolicyID == "" {
		claim.PolicyID = canonical.Policy.ID
	}
	if canonical.TariffContent != nil {
		claim.TariffContentRef = canonical.TariffContent.ContentRef
		claim.TariffContentHash = canonical.TariffContent.ContentHash
	}
	if canonical.PolicyContent != nil {
		claim.PolicyContentRef = canonical.PolicyContent.ContentRef
		claim.PolicyContentHash = canonical.PolicyContent.ContentHash
	}
	if err := claim.Validate(); err != nil {
		return SubmissionFeeClaim{}, false, err
	}
	return claim, true, nil
}

type submissionFeeLineIdentity struct {
	ID                string
	RuleID            string
	ItemID            string
	FixedFee          economics.FixedFeeIdentity
	Amount            *metering.Decimal
	AmountNumerator   string
	AmountDenominator string
	RoundedAmount     economics.Money
	RoundingScope     economics.RoundingScope
	RoundingPolicy    economics.RoundingPolicy
	IncludedUnit      bool
	Status            economics.RatingLineStatus
	ChargeKind        string
}

type submissionFeeSnapshotIdentity struct {
	ID          string
	Version     string
	ContentRef  string
	ContentHash string
}

type submissionFeeContextIdentity struct {
	Version              uint32
	Rater                submissionFeeSnapshotIdentity
	Tariff               submissionFeeSnapshotIdentity
	Policy               submissionFeeSnapshotIdentity
	QualifierSnapshot    string
	QualifierContentRef  string
	QualifierContentHash string
	EffectiveQualifiers  []metering.Dimension
	Lines                []submissionFeeLineIdentity
}

func submissionFeeContextFingerprint(valuation economics.Valuation, lines []submissionFeeLineIdentity) (string, error) {
	identity := submissionFeeContextIdentity{
		Version:           valuation.Version,
		Rater:             submissionFeeSnapshotIdentity{ID: valuation.Rater.ID, Version: valuation.Rater.Version},
		Tariff:            submissionFeeSnapshotIdentity{ID: valuation.Tariff.ID, Version: valuation.Tariff.Version},
		Policy:            submissionFeeSnapshotIdentity{ID: valuation.Policy.ID + "\x00" + valuation.Policy.PolicyID, Version: valuation.Policy.Version},
		QualifierSnapshot: valuation.QualifierSnapshot, EffectiveQualifiers: append([]metering.Dimension(nil), valuation.EffectiveQualifiers...),
		Lines: append([]submissionFeeLineIdentity(nil), lines...),
	}
	if valuation.RaterContent != nil {
		identity.Rater.ContentRef, identity.Rater.ContentHash = valuation.RaterContent.ContentRef, valuation.RaterContent.ContentHash
	}
	if valuation.TariffContent != nil {
		identity.Tariff.ContentRef, identity.Tariff.ContentHash = valuation.TariffContent.ContentRef, valuation.TariffContent.ContentHash
	}
	if valuation.PolicyContent != nil {
		identity.Policy.ContentRef, identity.Policy.ContentHash = valuation.PolicyContent.ContentRef, valuation.PolicyContent.ContentHash
	}
	if valuation.QualifierSnapshotRef != nil {
		identity.QualifierContentRef, identity.QualifierContentHash = valuation.QualifierSnapshotRef.ContentRef, valuation.QualifierSnapshotRef.ContentHash
	}
	payload, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("%w: context identity: %v", ErrSubmissionFeeInvalid, err)
	}
	sum := sha256.Sum256(payload)
	return "submission-fee-context:v1:" + hex.EncodeToString(sum[:]), nil
}
