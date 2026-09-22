package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Durable per-operation posting ownership pins for Task 17.3 subtask B1
// (Migration Strategy step 6).
//
// One pin per logical financial operation pins exactly one posting
// version/epoch (one owner, v1 or v2) with the marker epoch/generation/version
// snapshot observed at acquisition. Namespaces reuse existing canonical
// source/operation identities, never weak caller strings:
//
//   - customer call settlement: CustomerSettlementSourceKey(account, call)
//   - provider charge/payable: ProviderCostSourceKey(CallLegUsageKey(call, bLeg)
//     [+ ":provider-charge:"+chargeID for provider-charge subjects])
//   - financial adjustment: deterministic head pin
//     "financial-adjustment:v1:"+hex(sha256(canonical account/call/head/subject))
//
// Pin key is (storeID, kind, operationKey). Owner reuses the durable writer
// lineage (HistoricalV1WriterVersion/V2WriterVersion). Marker snapshot records
// the cutover version/epoch/generation/state at acquisition; exact replay
// (same kind/key/owner) is idempotent. Owner/epoch/identity mismatch is
// conflict/fence. Completion records the posting outcome (operation/transaction)
// so a waking worker distinguishes unposted pinned work from already completed
// effects without a second authority.
//
// Acquisition validates the current marker allows the owner:
//
//   - v1_active/v2_shadow permit V1 new; V2 new is fenced.
//   - v1_draining forbids all new; only replay/drain of already pinned V1
//     (Acquire replay + Complete of pinned V1) is allowed.
//   - v2_active permits V2 new/replay/complete; V1 new and V1 pinned replay/
//     complete are fenced; only exact V1 completed history replays (Acquire or
//     Complete with identical outcome) as idempotent history.
//
// Terminal claim/worker/admission wiring belongs to later 17.3B2 and must not
// be added here. No SQL, no provider SDKs, no globals/DI; small
// consumer-owned contracts only.

type PostingOperationKind string

const (
	PostingOperationCustomerSettlement  PostingOperationKind = "customer_call_settlement"
	PostingOperationProviderCharge      PostingOperationKind = "provider_charge"
	PostingOperationFinancialAdjustment PostingOperationKind = "financial_adjustment"
)

// Posting owners reuse the durable writer lineage so later claim fencing
// compares against the same ownership vocabulary used by historical readers.
const (
	PostingOwnerV1 = HistoricalV1WriterVersion
	PostingOwnerV2 = V2WriterVersion
)

type PostingPinStatus string

const (
	PostingPinPinned    PostingPinStatus = "pinned"
	PostingPinCompleted PostingPinStatus = "completed"
)

const (
	postingOwnershipMaxStoreIDLen      = 256
	postingOwnershipMaxOperationKeyLen = 1024
	postingOwnershipMaxCompletionLen   = 1024
	postingOwnershipMaxHeadKeyLen      = 512
)

var (
	ErrPostingOwnershipInvalid  = errors.New("billing: invalid posting ownership")
	ErrPostingOwnershipFence    = errors.New("billing: posting ownership fence conflict")
	ErrPostingOwnershipConflict = errors.New("billing: posting ownership conflict")
	ErrPostingOwnershipNotFound = errors.New("billing: posting ownership not found")
)

// Valid reports whether the operation kind is one of the three approved pin
// namespaces.
func (k PostingOperationKind) Valid() bool {
	switch k {
	case PostingOperationCustomerSettlement, PostingOperationProviderCharge, PostingOperationFinancialAdjustment:
		return true
	default:
		return false
	}
}

// Valid reports whether the pin status is pinned or completed.
func (s PostingPinStatus) Valid() bool {
	switch s {
	case PostingPinPinned, PostingPinCompleted:
		return true
	default:
		return false
	}
}

// PostingPin is the durable per-operation ownership record.
type PostingPin struct {
	StoreID                 string
	Kind                    PostingOperationKind
	OperationKey            string
	AccountID               string
	CallID                  BillingCallID
	BLegID                  string
	ProviderChargeID        string
	HeadKey                 string
	Subject                 metering.SubjectRef
	Owner                   string
	MarkerVersion           uint64
	MarkerEpoch             uint64
	MarkerGeneration        int
	MarkerState             AccountingCutoverState
	Status                  PostingPinStatus
	CompletionOperationKey  string
	CompletionTransactionID string
	CreatedAtUnix           int64
	UpdatedAtUnix           int64
	CompletedAtUnix         int64
}

// IsCompleted reports whether the pin carries a durable posting outcome.
func (p PostingPin) IsCompleted() bool {
	return p.Status == PostingPinCompleted
}

// AcquirePostingPinRequest pins one logical operation to one owner under the
// caller's observed marker epoch. The store derives the canonical operation
// key; callers never supply weak IDs.
type AcquirePostingPinRequest struct {
	Kind                  PostingOperationKind
	AccountID             string
	CallID                BillingCallID
	Subject               metering.SubjectRef
	HeadKey               string
	Owner                 string
	ExpectedMarkerVersion uint64
	ExpectedMarkerEpoch   uint64
}

// CompletePostingPinRequest records the posting outcome for a pinned operation.
// CompletionOperationKey is the durable journal/snapshot operation identity;
// CompletionTransactionID is the journal transaction when one exists and may
// be empty for legitimate no-journal (zero-amount) postings.
type CompletePostingPinRequest struct {
	Kind                    PostingOperationKind
	AccountID               string
	CallID                  BillingCallID
	Subject                 metering.SubjectRef
	HeadKey                 string
	Owner                   string
	ExpectedMarkerVersion   uint64
	ExpectedMarkerEpoch     uint64
	CompletionOperationKey  string
	CompletionTransactionID string
}

func validatePostingStoreID(storeID string) error {
	trimmed := strings.TrimSpace(storeID)
	if trimmed == "" {
		return fmt.Errorf("%w: %w: store scope is required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if len(trimmed) > postingOwnershipMaxStoreIDLen {
		return fmt.Errorf("%w: %w: store scope exceeds %d bytes", ErrPostingOwnershipInvalid, ErrInvalidRecord, postingOwnershipMaxStoreIDLen)
	}
	if trimmed != storeID {
		return fmt.Errorf("%w: %w: store scope must be trimmed", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if strings.ContainsAny(storeID, "\x00\r\n") {
		return fmt.Errorf("%w: %w: store scope contains control characters", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	return nil
}

func validatePostingOperationKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("%w: %w: operation key is required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if len(key) > postingOwnershipMaxOperationKeyLen {
		return fmt.Errorf("%w: %w: operation key exceeds %d bytes", ErrPostingOwnershipInvalid, ErrInvalidRecord, postingOwnershipMaxOperationKeyLen)
	}
	if strings.TrimSpace(key) != key {
		return fmt.Errorf("%w: %w: operation key must be trimmed", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if strings.ContainsAny(key, "\x00\r\n") {
		return fmt.Errorf("%w: %w: operation key contains control characters", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	return nil
}

func validatePostingCompletionKey(field, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%w: %w: %s is required", ErrPostingOwnershipInvalid, ErrInvalidRecord, field)
		}
		return nil
	}
	if len(value) > postingOwnershipMaxCompletionLen {
		return fmt.Errorf("%w: %w: %s exceeds %d bytes", ErrPostingOwnershipInvalid, ErrInvalidRecord, field, postingOwnershipMaxCompletionLen)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %w: %s must be trimmed", ErrPostingOwnershipInvalid, ErrInvalidRecord, field)
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%w: %w: %s contains control characters", ErrPostingOwnershipInvalid, ErrInvalidRecord, field)
	}
	return nil
}

func validatePostingOwner(owner string) error {
	if owner != PostingOwnerV1 && owner != PostingOwnerV2 {
		return fmt.Errorf("%w: %w: unknown posting owner %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, owner)
	}
	return nil
}

func validatePostingExpectedMarker(version, epoch uint64) error {
	if version == 0 || version > math.MaxInt64 {
		return fmt.Errorf("%w: %w: expected marker version %d out of range", ErrPostingOwnershipInvalid, ErrInvalidRecord, version)
	}
	if epoch == 0 || epoch > math.MaxInt64 {
		return fmt.Errorf("%w: %w: expected marker epoch %d out of range", ErrPostingOwnershipInvalid, ErrInvalidRecord, epoch)
	}
	return nil
}

func validatePostingHeadKey(headKey string) error {
	if strings.TrimSpace(headKey) == "" {
		return fmt.Errorf("%w: %w: head key is required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if strings.TrimSpace(headKey) != headKey {
		return fmt.Errorf("%w: %w: head key must not carry surrounding whitespace", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if len(headKey) > postingOwnershipMaxHeadKeyLen {
		return fmt.Errorf("%w: %w: head key exceeds %d bytes", ErrPostingOwnershipInvalid, ErrInvalidRecord, postingOwnershipMaxHeadKeyLen)
	}
	if err := economics.ValidateSafeRef("posting head key", headKey); err != nil {
		return fmt.Errorf("%w: %w: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	return nil
}

// validatePostingSubject checks the request-scoped B-leg/provider-charge
// subject and its account/call lineage against the pin identity.
func validatePostingSubject(subject metering.SubjectRef, accountID string, callID BillingCallID) error {
	if err := subject.Validate(); err != nil {
		return fmt.Errorf("%w: %w: subject: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	if subject.Kind != metering.SubjectBLeg && subject.Kind != metering.SubjectProviderCharge {
		return fmt.Errorf("%w: %w: request-scoped B-leg/provider-charge subject required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if subject.StoreID == "" {
		return fmt.Errorf("%w: %w: subject store scope is required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if subject.AccountID != "" && subject.AccountID != accountID {
		return fmt.Errorf("%w: %w: subject account differs from pin account", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if subject.BillingCallID != "" && subject.BillingCallID != callID.String() {
		return fmt.Errorf("%w: %w: subject billing call differs from pin call", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if subject.CallID != "" && subject.CallID != callID.String() {
		return fmt.Errorf("%w: %w: subject call differs from pin call", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	return nil
}

// CustomerPostingOperationKey derives the canonical customer settlement pin
// key. It reuses CustomerSettlementSourceKey so the pin and the settlement
// posting share one identity.
func CustomerPostingOperationKey(accountID string, callID BillingCallID) (string, error) {
	key, err := CustomerSettlementSourceKey(accountID, callID)
	if err != nil {
		return "", fmt.Errorf("%w: %w: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	if err := validatePostingOperationKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// ProviderPostingOperationKey derives the canonical provider charge pin key
// from the request-scoped subject. B-leg subjects pin the leg lineage;
// provider-charge subjects pin the charge lineage beneath that leg.
func ProviderPostingOperationKey(storeID, accountID string, callID BillingCallID, subject metering.SubjectRef) (string, error) {
	if err := validatePostingStoreID(storeID); err != nil {
		return "", err
	}
	if !validEconomicIdentity(accountID, metering.MaxSchemaIDBytes) {
		return "", fmt.Errorf("%w: %w: account id is required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if err := callID.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	if err := validatePostingSubject(subject, accountID, callID); err != nil {
		return "", err
	}
	if subject.StoreID != storeID {
		return "", fmt.Errorf("%w: %w: subject store %q differs from pin store %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, subject.StoreID, storeID)
	}
	legKey, err := CallLegUsageKey(callID, subject.BLegID)
	if err != nil {
		return "", fmt.Errorf("%w: %w: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	lineage := legKey
	if subject.Kind == metering.SubjectProviderCharge {
		chargeID := strings.TrimSpace(subject.ProviderChargeID)
		if chargeID == "" {
			return "", fmt.Errorf("%w: %w: provider charge identity is required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		if strings.Contains(chargeID, ":") {
			return "", fmt.Errorf("%w: %w: provider charge identity must not contain ':'", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		lineage = legKey + ":provider-charge:" + chargeID
	}
	key, err := ProviderCostSourceKey(lineage)
	if err != nil {
		return "", fmt.Errorf("%w: %w: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	if err := validatePostingOperationKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// FinancialAdjustmentPostingOperationKey derives the deterministic head pin
// key from the canonical adjustment identity (account/call/head/subject). The
// sha256 preimage carries an explicit version so head pins never collide with
// settlement or provider keys. B2b4 keeps this selected-cost head identity
// unchanged; cost pass-through and direct adjustments use their own
// versioned preimages below.
func FinancialAdjustmentPostingOperationKey(storeID, accountID string, callID BillingCallID, headKey string, subject metering.SubjectRef) (string, error) {
	if err := validatePostingStoreID(storeID); err != nil {
		return "", err
	}
	if !validEconomicIdentity(accountID, metering.MaxSchemaIDBytes) {
		return "", fmt.Errorf("%w: %w: account id is required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if err := callID.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	if err := validatePostingHeadKey(headKey); err != nil {
		return "", err
	}
	if err := validatePostingSubject(subject, accountID, callID); err != nil {
		return "", err
	}
	if subject.StoreID != storeID {
		return "", fmt.Errorf("%w: %w: subject store %q differs from pin store %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, subject.StoreID, storeID)
	}
	payload, err := json.Marshal(struct {
		Version   string              `json:"version"`
		StoreID   string              `json:"store_id"`
		AccountID string              `json:"account_id"`
		CallID    string              `json:"call_id"`
		HeadKey   string              `json:"head_key"`
		Subject   metering.SubjectRef `json:"subject"`
	}{"financial-adjustment-pin:v1", storeID, accountID, callID.String(), headKey, subject})
	if err != nil {
		return "", fmt.Errorf("%w: %w: adjustment pin identity: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	digest := sha256.Sum256(payload)
	key := "financial-adjustment:v1:" + hex.EncodeToString(digest[:])
	if err := validatePostingOperationKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// CostPassThroughHeadKey derives the canonical customer pass-through head
// identity for one account/call. The store persists the same key; the pin
// reuses it so head lineage never requires fake B-leg data.
func CostPassThroughHeadKey(accountID string, callID BillingCallID) string {
	return "cost-pass-through-head:v1:" + strings.TrimSpace(accountID) + ":" + callID.String()
}

// CostPassThroughFinancialAdjustmentPostingOperationKey derives the
// deterministic per-head pin for synchronous cost pass-through revisions.
// Preimage is exactly the canonical head lineage (store/account/call/head)
// with explicit version "cost-pass-through-adjustment-pin:v1". One head pin
// remains authoritative for the replacement chain: each revision keeps its
// own canonical source/journal identity while pin completion advances to the
// latest outcome under the same owner. No B-leg/subject is required.
func CostPassThroughFinancialAdjustmentPostingOperationKey(storeID, accountID string, callID BillingCallID) (string, error) {
	if err := validatePostingStoreID(storeID); err != nil {
		return "", err
	}
	if !validEconomicIdentity(accountID, metering.MaxSchemaIDBytes) {
		return "", fmt.Errorf("%w: %w: account id is required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if err := callID.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	headKey := CostPassThroughHeadKey(accountID, callID)
	if err := validatePostingHeadKey(headKey); err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Version   string `json:"version"`
		StoreID   string `json:"store_id"`
		AccountID string `json:"account_id"`
		CallID    string `json:"call_id"`
		HeadKey   string `json:"head_key"`
	}{"cost-pass-through-adjustment-pin:v1", storeID, accountID, callID.String(), headKey})
	if err != nil {
		return "", fmt.Errorf("%w: %w: cost pass-through pin identity: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	digest := sha256.Sum256(payload)
	key := "financial-adjustment-cost-pass-through:v1:" + hex.EncodeToString(digest[:])
	if err := validatePostingOperationKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// DirectFinancialAdjustmentPostingOperationKey derives the deterministic
// per-source pin for synchronous direct adjustments (PostAdjustment). Preimage
// is exactly the existing canonical idempotency identity: the scoped
// adjustment operation key ScopedOperationKey("adjustment", account, source)
// plus store/account, with explicit version "direct-adjustment-pin:v1". No
// call/head/B-leg lineage is required; the pin row carries the caller source
// in head_key so durability remains recomputable without fake lineage.
func DirectFinancialAdjustmentPostingOperationKey(storeID, accountID, sourceKey string) (string, error) {
	if err := validatePostingStoreID(storeID); err != nil {
		return "", err
	}
	if !validEconomicIdentity(accountID, metering.MaxSchemaIDBytes) {
		return "", fmt.Errorf("%w: %w: account id is required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	trimmed := strings.TrimSpace(sourceKey)
	if trimmed == "" || trimmed != sourceKey {
		return "", fmt.Errorf("%w: %w: adjustment source key is required and must be trimmed", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if strings.ContainsAny(sourceKey, "\x00\r\n") {
		return "", fmt.Errorf("%w: %w: adjustment source key contains control characters", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if len(sourceKey) > postingOwnershipMaxOperationKeyLen {
		return "", fmt.Errorf("%w: %w: adjustment source key exceeds %d bytes", ErrPostingOwnershipInvalid, ErrInvalidRecord, postingOwnershipMaxOperationKeyLen)
	}
	scoped := ScopedOperationKey("adjustment", accountID, sourceKey)
	payload, err := json.Marshal(struct {
		Version   string `json:"version"`
		StoreID   string `json:"store_id"`
		AccountID string `json:"account_id"`
		ScopedKey string `json:"scoped_operation_key"`
	}{"direct-adjustment-pin:v1", storeID, accountID, scoped})
	if err != nil {
		return "", fmt.Errorf("%w: %w: direct adjustment pin identity: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	digest := sha256.Sum256(payload)
	key := "financial-adjustment-direct:v1:" + hex.EncodeToString(digest[:])
	if err := validatePostingOperationKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// IsSelectedCostAdjustmentPinKey reports whether key is a B2b3 selected-cost
// head pin (unchanged identity).
func IsSelectedCostAdjustmentPinKey(key string) bool {
	return strings.HasPrefix(key, "financial-adjustment:v1:")
}

// IsCostPassThroughAdjustmentPinKey reports whether key is a B2b4
// cost-pass-through per-head pin.
func IsCostPassThroughAdjustmentPinKey(key string) bool {
	return strings.HasPrefix(key, "financial-adjustment-cost-pass-through:v1:")
}

// IsDirectAdjustmentPinKey reports whether key is a B2b4 direct-adjustment
// per-source pin.
func IsDirectAdjustmentPinKey(key string) bool {
	return strings.HasPrefix(key, "financial-adjustment-direct:v1:")
}

func validateAcquireIdentity(req AcquirePostingPinRequest) error {
	if !req.Kind.Valid() {
		return fmt.Errorf("%w: %w: unknown operation kind %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, string(req.Kind))
	}
	if !validEconomicIdentity(req.AccountID, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: %w: account id is required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if err := req.CallID.Validate(); err != nil {
		return fmt.Errorf("%w: %w: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	if err := validatePostingOwner(req.Owner); err != nil {
		return err
	}
	if err := validatePostingExpectedMarker(req.ExpectedMarkerVersion, req.ExpectedMarkerEpoch); err != nil {
		return err
	}
	zeroSubject := req.Subject.Kind == ""
	switch req.Kind {
	case PostingOperationCustomerSettlement:
		if !zeroSubject {
			return fmt.Errorf("%w: %w: customer settlement carries no subject", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		if strings.TrimSpace(req.HeadKey) != "" {
			return fmt.Errorf("%w: %w: customer settlement carries no head", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
	case PostingOperationProviderCharge:
		if zeroSubject {
			return fmt.Errorf("%w: %w: provider charge requires a subject", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		if strings.TrimSpace(req.HeadKey) != "" {
			return fmt.Errorf("%w: %w: provider charge carries no head", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		if err := validatePostingSubject(req.Subject, req.AccountID, req.CallID); err != nil {
			return err
		}
	case PostingOperationFinancialAdjustment:
		if zeroSubject {
			return fmt.Errorf("%w: %w: adjustment requires a subject", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		if err := validatePostingHeadKey(req.HeadKey); err != nil {
			return err
		}
		if err := validatePostingSubject(req.Subject, req.AccountID, req.CallID); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: %w: unknown operation kind %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, string(req.Kind))
	}
	return nil
}

// Validate fails closed on malformed pin acquisition requests.
func (r AcquirePostingPinRequest) Validate() error {
	return validateAcquireIdentity(r)
}

// Validate fails closed on malformed pin completion requests.
func (r CompletePostingPinRequest) Validate() error {
	acquire := AcquirePostingPinRequest{Kind: r.Kind, AccountID: r.AccountID, CallID: r.CallID, Subject: r.Subject, HeadKey: r.HeadKey, Owner: r.Owner, ExpectedMarkerVersion: r.ExpectedMarkerVersion, ExpectedMarkerEpoch: r.ExpectedMarkerEpoch}
	if err := validateAcquireIdentity(acquire); err != nil {
		return err
	}
	if err := validatePostingCompletionKey("completion operation key", r.CompletionOperationKey, true); err != nil {
		return err
	}
	if err := validatePostingCompletionKey("completion transaction id", r.CompletionTransactionID, false); err != nil {
		return err
	}
	return nil
}

// Validate fails closed on any malformed or inconsistent pin. OperationKey
// must equal the canonical derivation for the pin identity so weak or
// cross-kind keys cannot become durable. Financial adjustment has three
// versioned shapes sharing one kind: selected-cost head pins
// ("financial-adjustment:v1:", B2b3, account/call/head/subject),
// cost-pass-through per-head pins
// ("financial-adjustment-cost-pass-through:v1:", B2b4, account/call/head, no
// subject) and direct per-source pins ("financial-adjustment-direct:v1:",
// B2b4, account/source in head_key, no call/subject).
func (p PostingPin) Validate() error {
	if err := validatePostingStoreID(p.StoreID); err != nil {
		return err
	}
	if !p.Kind.Valid() {
		return fmt.Errorf("%w: %w: unknown operation kind %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, string(p.Kind))
	}
	if err := validatePostingOperationKey(p.OperationKey); err != nil {
		return err
	}
	if !validEconomicIdentity(p.AccountID, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: %w: account id is required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	// Call lineage is required except for direct-adjustment pins, which carry
	// no call (empty call_id in storage). Validate per shape below.
	isDirect := p.Kind == PostingOperationFinancialAdjustment && IsDirectAdjustmentPinKey(p.OperationKey)
	if !isDirect {
		if err := p.CallID.Validate(); err != nil {
			return fmt.Errorf("%w: %w: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
		}
	} else if strings.TrimSpace(p.CallID.String()) != "" {
		return fmt.Errorf("%w: %w: direct adjustment carries no call lineage", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if err := validatePostingOwner(p.Owner); err != nil {
		return err
	}
	if p.MarkerVersion == 0 || p.MarkerVersion > math.MaxInt64 {
		return fmt.Errorf("%w: %w: marker version %d out of range", ErrPostingOwnershipInvalid, ErrInvalidRecord, p.MarkerVersion)
	}
	if p.MarkerEpoch == 0 || p.MarkerEpoch > math.MaxInt64 {
		return fmt.Errorf("%w: %w: marker epoch %d out of range", ErrPostingOwnershipInvalid, ErrInvalidRecord, p.MarkerEpoch)
	}
	if !p.MarkerState.Valid() {
		return fmt.Errorf("%w: %w: unknown marker state %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, string(p.MarkerState))
	}
	gen, owner, _, ok := AccountingCutoverExpectations(p.MarkerState)
	if !ok {
		return fmt.Errorf("%w: %w: unknown marker state %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, string(p.MarkerState))
	}
	if p.MarkerGeneration != gen {
		return fmt.Errorf("%w: %w: marker state %q requires generation %d, got %d", ErrPostingOwnershipInvalid, ErrInvalidRecord, string(p.MarkerState), gen, p.MarkerGeneration)
	}
	if p.Owner != owner {
		return fmt.Errorf("%w: %w: marker state %q requires pin owner %q, got %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, string(p.MarkerState), owner, p.Owner)
	}
	if !p.Status.Valid() {
		return fmt.Errorf("%w: %w: unknown pin status %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, string(p.Status))
	}
	zeroSubject := p.Subject.Kind == ""
	var wantKey string
	var err error
	switch p.Kind {
	case PostingOperationCustomerSettlement:
		if !zeroSubject {
			return fmt.Errorf("%w: %w: customer settlement carries no subject", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		if strings.TrimSpace(p.BLegID) != "" || strings.TrimSpace(p.ProviderChargeID) != "" || strings.TrimSpace(p.HeadKey) != "" {
			return fmt.Errorf("%w: %w: customer settlement carries no leg/head identity", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		wantKey, err = CustomerPostingOperationKey(p.AccountID, p.CallID)
	case PostingOperationProviderCharge:
		if zeroSubject {
			return fmt.Errorf("%w: %w: provider charge requires a subject", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		if strings.TrimSpace(p.HeadKey) != "" {
			return fmt.Errorf("%w: %w: provider charge carries no head", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		if err := validatePostingSubject(p.Subject, p.AccountID, p.CallID); err != nil {
			return err
		}
		if p.Subject.StoreID != p.StoreID {
			return fmt.Errorf("%w: %w: subject store differs from pin store", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		if p.BLegID != p.Subject.BLegID {
			return fmt.Errorf("%w: %w: pin B-leg differs from subject", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		if p.ProviderChargeID != p.Subject.ProviderChargeID {
			return fmt.Errorf("%w: %w: pin charge differs from subject", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		// F5: provider revisions use immutable revision-specific pins
		// (provider_call_cogs:v1:... derived from ProviderCostRevisionSourceKey),
		// while base legacy charges keep lineage pins (provider-cost:v1:...).
		// Revision pins are canonical digests of immutable revision identity;
		// creation enforces exact derivation from input/work, so validation
		// accepts the versioned prefix without recomputing the digest from
		// pin fields (which carry lineage only, not head/revision/hash).
		if IsProviderRevisionPinKey(p.OperationKey) {
			if err := validatePostingOperationKey(p.OperationKey); err != nil {
				return err
			}
			wantKey = p.OperationKey
			err = nil
			break
		}
		wantKey, err = ProviderPostingOperationKey(p.StoreID, p.AccountID, p.CallID, p.Subject)
	case PostingOperationFinancialAdjustment:
		// Dispatch on versioned operation-key prefix so selected-cost head
		// identity stays unchanged while sync shapes avoid fake B-leg/head.
		switch {
		case IsCostPassThroughAdjustmentPinKey(p.OperationKey):
			if !zeroSubject {
				return fmt.Errorf("%w: %w: cost pass-through carries no subject", ErrPostingOwnershipInvalid, ErrInvalidRecord)
			}
			if strings.TrimSpace(p.BLegID) != "" || strings.TrimSpace(p.ProviderChargeID) != "" {
				return fmt.Errorf("%w: %w: cost pass-through carries no leg identity", ErrPostingOwnershipInvalid, ErrInvalidRecord)
			}
			wantHead := CostPassThroughHeadKey(p.AccountID, p.CallID)
			if p.HeadKey != wantHead {
				return fmt.Errorf("%w: %w: cost pass-through head %q differs from canonical %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, p.HeadKey, wantHead)
			}
			wantKey, err = CostPassThroughFinancialAdjustmentPostingOperationKey(p.StoreID, p.AccountID, p.CallID)
		case IsDirectAdjustmentPinKey(p.OperationKey):
			if !zeroSubject {
				return fmt.Errorf("%w: %w: direct adjustment carries no subject", ErrPostingOwnershipInvalid, ErrInvalidRecord)
			}
			if strings.TrimSpace(p.BLegID) != "" || strings.TrimSpace(p.ProviderChargeID) != "" {
				return fmt.Errorf("%w: %w: direct adjustment carries no leg identity", ErrPostingOwnershipInvalid, ErrInvalidRecord)
			}
			// Direct pins reuse head_key to carry the caller source key so the
			// hash remains recomputable without fake call/head lineage.
			sourceKey := strings.TrimSpace(p.HeadKey)
			if sourceKey == "" || sourceKey != p.HeadKey {
				return fmt.Errorf("%w: %w: direct adjustment source is required and must be trimmed", ErrPostingOwnershipInvalid, ErrInvalidRecord)
			}
			if strings.ContainsAny(sourceKey, "\x00\r\n") {
				return fmt.Errorf("%w: %w: direct adjustment source contains control characters", ErrPostingOwnershipInvalid, ErrInvalidRecord)
			}
			wantKey, err = DirectFinancialAdjustmentPostingOperationKey(p.StoreID, p.AccountID, sourceKey)
		default:
			if zeroSubject {
				return fmt.Errorf("%w: %w: adjustment requires a subject", ErrPostingOwnershipInvalid, ErrInvalidRecord)
			}
			if err := validatePostingHeadKey(p.HeadKey); err != nil {
				return err
			}
			if err := validatePostingSubject(p.Subject, p.AccountID, p.CallID); err != nil {
				return err
			}
			if p.Subject.StoreID != p.StoreID {
				return fmt.Errorf("%w: %w: subject store differs from pin store", ErrPostingOwnershipInvalid, ErrInvalidRecord)
			}
			if p.BLegID != p.Subject.BLegID {
				return fmt.Errorf("%w: %w: pin B-leg differs from subject", ErrPostingOwnershipInvalid, ErrInvalidRecord)
			}
			if p.ProviderChargeID != p.Subject.ProviderChargeID {
				return fmt.Errorf("%w: %w: pin charge differs from subject", ErrPostingOwnershipInvalid, ErrInvalidRecord)
			}
			wantKey, err = FinancialAdjustmentPostingOperationKey(p.StoreID, p.AccountID, p.CallID, p.HeadKey, p.Subject)
		}
	default:
		return fmt.Errorf("%w: %w: unknown operation kind %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, string(p.Kind))
	}
	if err != nil {
		return err
	}
	if wantKey != p.OperationKey {
		return fmt.Errorf("%w: %w: operation key does not match canonical identity", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if p.CreatedAtUnix <= 0 || p.CreatedAtUnix > math.MaxInt64 {
		return fmt.Errorf("%w: %w: pin timestamps must be positive", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if p.UpdatedAtUnix <= 0 || p.UpdatedAtUnix > math.MaxInt64 {
		return fmt.Errorf("%w: %w: pin timestamps must be positive", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if p.UpdatedAtUnix < p.CreatedAtUnix {
		return fmt.Errorf("%w: %w: pin updated_at precedes created_at", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	switch p.Status {
	case PostingPinPinned:
		if strings.TrimSpace(p.CompletionOperationKey) != "" || strings.TrimSpace(p.CompletionTransactionID) != "" {
			return fmt.Errorf("%w: %w: unposted pin carries no completion outcome", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		if p.CompletedAtUnix != 0 {
			return fmt.Errorf("%w: %w: unposted pin carries no completion time", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
	case PostingPinCompleted:
		if err := validatePostingCompletionKey("completion operation key", p.CompletionOperationKey, true); err != nil {
			return err
		}
		if err := validatePostingCompletionKey("completion transaction id", p.CompletionTransactionID, false); err != nil {
			return err
		}
		if p.CompletedAtUnix <= 0 || p.CompletedAtUnix > math.MaxInt64 {
			return fmt.Errorf("%w: %w: pin completion time must be positive", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
		if p.CompletedAtUnix < p.CreatedAtUnix || p.UpdatedAtUnix < p.CompletedAtUnix {
			return fmt.Errorf("%w: %w: pin completion ordering violates created/completed/updated", ErrPostingOwnershipInvalid, ErrInvalidRecord)
		}
	default:
		return fmt.Errorf("%w: %w: unknown pin status %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, string(p.Status))
	}
	return nil
}

// IsPostingOwnerAllowedForNew reports whether a fresh (unpinned) operation may
// be pinned to owner under state. Draining forbids all new; v2_active permits
// only V2 new.
func IsPostingOwnerAllowedForNew(state AccountingCutoverState, owner string) bool {
	switch state {
	case AccountingCutoverV1Active, AccountingCutoverV2Shadow:
		return owner == PostingOwnerV1
	case AccountingCutoverV1Draining:
		return false
	case AccountingCutoverV2Active:
		return owner == PostingOwnerV2
	default:
		return false
	}
}

// IsPostingPinAcquireReplayAllowed reports whether an already pinned operation
// with the same owner may be re-acquired (idempotent replay) under the current
// marker. Draining replays pinned V1; v2_active replays V2 and exact V1
// completed history only.
func IsPostingPinAcquireReplayAllowed(currentState AccountingCutoverState, pin PostingPin) bool {
	switch pin.Owner {
	case PostingOwnerV1:
		switch currentState {
		case AccountingCutoverV1Active, AccountingCutoverV2Shadow, AccountingCutoverV1Draining:
			return true
		case AccountingCutoverV2Active:
			return pin.Status == PostingPinCompleted
		default:
			return false
		}
	case PostingOwnerV2:
		return currentState == AccountingCutoverV2Active
	default:
		return false
	}
}

// IsPostingPinCompleteAllowed reports whether a pinned (uncompleted) operation
// may transition to completed under the current marker. Completed-history
// replay bypasses this check; callers handle that before consulting it.
func IsPostingPinCompleteAllowed(currentState AccountingCutoverState, pin PostingPin) bool {
	if pin.Status != PostingPinPinned {
		return false
	}
	switch pin.Owner {
	case PostingOwnerV1:
		switch currentState {
		case AccountingCutoverV1Active, AccountingCutoverV2Shadow, AccountingCutoverV1Draining:
			return true
		default:
			return false
		}
	case PostingOwnerV2:
		return currentState == AccountingCutoverV2Active
	default:
		return false
	}
}

// IsPostingPinReplay reports whether req is the exact durable replay of pin:
// same kind, same canonical operation key, same owner. Marker snapshots are
// intentionally excluded so draining replays succeed after the marker
// advances; staleness is fenced separately via ExpectedMarkerVersion/Epoch.
func IsPostingPinReplay(pin PostingPin, req AcquirePostingPinRequest, operationKey string) bool {
	return pin.Kind == req.Kind && pin.OperationKey == operationKey && pin.Owner == req.Owner
}
