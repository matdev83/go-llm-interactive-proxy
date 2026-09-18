package billing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Rolling A-leg economic snapshots (refinement Task 5.3, Cycle 1).
//
// An A-leg report is a CURRENT rolling projection over durable B-leg
// evidence, never a final session bill. AsOf is an output observation
// timestamp for the returned snapshot, not a caller-provided historical
// cutoff. There is no historical time travel, no finality boolean, and no
// global revision.
//
// The read side is pure: it never rates, settles, appends journal entries,
// changes balances, mutates queues, or alters lifecycle state. The customer
// plane carries proven settlement economics; provider COGS and exclusions
// stay pending in Cycle 1 and are never faked as known zero. Totals are
// native-currency only with no implicit FX, and unknown amounts are never
// coerced to zero. Existing resource allocations remain separate subjects
// and are never folded into synthetic B-legs.

const (
	// ALegReportDefaultLimit bounds one snapshot page when the caller omits a limit.
	ALegReportDefaultLimit = 100
	// ALegReportMaxLimit is the hard bounded page maximum.
	ALegReportMaxLimit = 500

	alegReportCursorPrefix  = "aleg."
	alegReportCursorVersion = 1
	alegReportCursorKind    = "aleg-report"
	// alegReportMaxCursorLength bounds opaque cursor input; production
	// cursors are an order of magnitude smaller.
	alegReportMaxCursorLength = 2048
)

// ALegReportQuery scopes one rolling snapshot page to StoreID (implicit in
// the serving store handle) + AccountID + ALegID. Cursor is opaque; callers
// must not construct or interpret it.
type ALegReportQuery struct {
	AccountID string
	ALegID    string
	Limit     int
	Cursor    string
}

// Normalize trims scope, applies the default limit, and rejects unbounded or
// unscoped queries. The opaque cursor is preserved byte-for-byte: callers
// must supply the exact token from a prior page, and the decoder at the
// store boundary rejects any non-canonical form, including whitespace.
func (q ALegReportQuery) Normalize() (ALegReportQuery, error) {
	out := q
	out.AccountID = strings.TrimSpace(q.AccountID)
	out.ALegID = strings.TrimSpace(q.ALegID)
	if out.AccountID == "" || out.ALegID == "" {
		return ALegReportQuery{}, fmt.Errorf("%w: A-leg report requires account and A-leg scope", ErrReportInvalid)
	}
	if out.Limit == 0 {
		out.Limit = ALegReportDefaultLimit
	}
	if out.Limit < 1 || out.Limit > ALegReportMaxLimit {
		return ALegReportQuery{}, fmt.Errorf("%w: A-leg report limit must be between 1 and %d", ErrReportInvalid, ALegReportMaxLimit)
	}
	return out, nil
}

// ALegCallStatus is the explicit per-call customer authority state. Pending
// means no settlement proof yet (the call may settle or resume later);
// unknown means conflicting or stray evidence with an attached issue.
// Unknown is never a zero amount.
type ALegCallStatus string

const (
	ALegCallKnown   ALegCallStatus = "known"
	ALegCallPending ALegCallStatus = "pending"
	ALegCallUnknown ALegCallStatus = "unknown"
)

// ALegLegProviderStatus is the explicit per-B-leg provider completeness
// state for Cycle 1: attributable legs stay pending (COGS deferred),
// while a proven never-started or rejected leg is a lineage zero with an
// explicit basis. Unknown is the absence of evidence, never zero.
type ALegLegProviderStatus string

const (
	ALegProviderPending   ALegLegProviderStatus = "pending"
	ALegProviderKnownZero ALegLegProviderStatus = "known_zero"
)

// ALegAdjustmentRef is one validated pass-through journal in the distinct
// customer adjustment plane. Amounts here never enter retail totals; the
// production-writer join is deferred to Cycle 2.
type ALegAdjustmentRef struct {
	TransactionID string
	Amount        Money
}

// ALegReportLeg is one durable B-leg row with its lineage.
type ALegReportLeg struct {
	BLegID         string
	Outcome        LegOutcome
	Surfaced       SurfacedState
	BackendID      string
	ProviderID     string
	ModelID        string
	Fingerprint    string
	ProviderStatus ALegLegProviderStatus
	// ZeroBasis is set only when ProviderStatus is known_zero and names the
	// explicit lineage basis (for example "never_started_not_billable").
	ZeroBasis string
}

// ALegReportContribution is one B-leg row on the leg stream with its parent
// call summary attached. Leg and call streams page independently under the
// one opaque cursor; consumers union pages by LegKey and CallID.
type ALegReportContribution struct {
	Call           ALegReportCallSummary
	LegKey         string
	BLegID         string
	Outcome        LegOutcome
	Surfaced       SurfacedState
	BackendID      string
	ProviderID     string
	ModelID        string
	Fingerprint    string
	ProviderStatus ALegLegProviderStatus
	ZeroBasis      string
}

// ALegReportCallSummary is the durable parent-call context for one call
// stream row. CustomerChargeKnown distinguishes a proven zero from an
// unproven call; MissingBLegIDs names expected B-leg identities with no
// durable leg row (explicit unresolved lineage, never a synthetic B-leg).
type ALegReportCallSummary struct {
	CallID               BillingCallID
	SessionID            string
	Outcome              TurnOutcome
	ExpectedBLegIDs      []string
	MissingBLegIDs       []string
	Status               ALegCallStatus
	CustomerCharge       Money
	CustomerChargeKnown  bool
	CustomerOperationKey string
	Adjustments          []ALegAdjustmentRef
}

// ALegReportCall is one call stream row: the summary plus the B-legs of
// that call present on the current leg page. A call with legs split across
// leg pages repeats across pages with disjoint leg subsets; union by CallID.
type ALegReportCall struct {
	ALegReportCallSummary
	BLegs []ALegReportLeg
}

// ALegRetailTotals is the native-currency customer plane: proven per-call
// charges only, never multiplied by B-leg count.
type ALegRetailTotals struct {
	Currency      string
	KnownSubtotal Money
	SettledCalls  int
	PendingCalls  int
	UnknownCalls  int
}

// ALegProviderTotals is the Cycle 1 operator plane: counts only, no proven
// economics yet. COGS and exclusions resolve in Cycle 2.
type ALegProviderTotals struct {
	Currency    string
	PendingLegs int
	ZeroLegs    int
}

// ALegReport is one page of a rolling A-leg snapshot. Totals and counts
// cover the whole scope; Contributions carries at most Limit B-leg rows and
// Calls carries at most Limit call summaries, each paged independently under
// the one opaque cursor. Closure-only calls and expected-but-missing B-legs
// stay discoverable through bounded traversal of the call stream. There is
// intentionally no finality marker: a resumed BillingCallID appears in a
// later snapshot.
type ALegReport struct {
	StoreID       string
	AccountID     string
	ALegID        string
	Currency      string
	AsOf          time.Time
	Contributions []ALegReportContribution
	Calls         []ALegReportCall
	// CallCount is the distinct BillingCallID count in scope, which may
	// exceed len(Calls) when calls span pages.
	CallCount  int
	Retail     ALegRetailTotals
	Provider   ALegProviderTotals
	Issues     []ReconciliationIssue
	NextCursor string
}

// ALegReportReader is the consumer-owned billing query seam for rolling
// A-leg snapshots. Implementations must serve the current snapshot inside
// one read-only transaction with a dialect-compatible repeatable snapshot,
// and must not mutate accounting state.
type ALegReportReader interface {
	QueryALegReport(context.Context, ALegReportQuery) (ALegReport, error)
}

type alegReportCursor struct {
	Version   int    `json:"v"`
	Kind      string `json:"kind"`
	StoreID   string `json:"store"`
	AccountID string `json:"account"`
	ALegID    string `json:"aleg"`
	// LegCall/LegBLeg is the B-leg stream position; Call is the independent
	// call stream position. A non-empty cursor always carries a call
	// position; the leg position is either fully populated or fully empty
	// (legs absent or exhausted). One-sided states are never encoded.
	LegCall string `json:"leg_call,omitempty"`
	LegBLeg string `json:"leg_bleg,omitempty"`
	Call    string `json:"call,omitempty"`
}

// EncodeALegReportCursor builds an opaque, scope-bound page cursor over
// durable BillingCallID (call stream) and (BillingCallID, B-leg) (B-leg
// stream) identities. Ordering never uses timestamps. Structurally partial
// positions encode to the empty page-1 token only when fully empty;
// anything else partial encodes to "" and must be treated as invalid.
func EncodeALegReportCursor(storeID, accountID, aLegID, legCall, legBLeg, call string) string {
	if legCall == "" && legBLeg == "" && call == "" {
		return ""
	}
	if (legCall == "") != (legBLeg == "") {
		return ""
	}
	if call == "" {
		return ""
	}
	payload, err := json.Marshal(alegReportCursor{
		Version: alegReportCursorVersion, Kind: alegReportCursorKind,
		StoreID: storeID, AccountID: accountID, ALegID: aLegID,
		LegCall: legCall, LegBLeg: legBLeg, Call: call,
	})
	if err != nil {
		return ""
	}
	return alegReportCursorPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

// DecodeALegReportCursor strictly validates an opaque cursor against the
// query scope and returns the two durable stream positions. An empty cursor
// is page 1. The raw token is never normalized: every structurally partial
// or inconsistent state — leading/trailing/interior whitespace, one-sided
// leg positions, legs without a call position, unsupported versions,
// mismatched scope, oversize raw input, malformed encoding, and
// non-canonical forms — is rejected with ErrReportInvalid.
func DecodeALegReportCursor(raw, storeID, accountID, aLegID string) (legCall, legBLeg, call string, err error) {
	if raw == "" {
		return "", "", "", nil
	}
	if len(raw) > alegReportMaxCursorLength {
		return "", "", "", fmt.Errorf("%w: oversized A-leg report cursor", ErrReportInvalid)
	}
	if !strings.HasPrefix(raw, alegReportCursorPrefix) {
		return "", "", "", fmt.Errorf("%w: malformed A-leg report cursor", ErrReportInvalid)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, alegReportCursorPrefix))
	if err != nil {
		return "", "", "", fmt.Errorf("%w: malformed A-leg report cursor", ErrReportInvalid)
	}
	var cursor alegReportCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return "", "", "", fmt.Errorf("%w: malformed A-leg report cursor", ErrReportInvalid)
	}
	if cursor.Version != alegReportCursorVersion || cursor.Kind != alegReportCursorKind {
		return "", "", "", fmt.Errorf("%w: unsupported A-leg report cursor", ErrReportInvalid)
	}
	if cursor.StoreID != storeID || cursor.AccountID != accountID || cursor.ALegID != aLegID {
		return "", "", "", fmt.Errorf("%w: A-leg report cursor scope mismatch", ErrReportInvalid)
	}
	if (cursor.LegCall == "") != (cursor.LegBLeg == "") {
		return "", "", "", fmt.Errorf("%w: partial A-leg report cursor leg position", ErrReportInvalid)
	}
	if cursor.Call == "" {
		return "", "", "", fmt.Errorf("%w: partial A-leg report cursor call position", ErrReportInvalid)
	}
	canonical, err := json.Marshal(cursor)
	if err != nil {
		return "", "", "", fmt.Errorf("%w: malformed A-leg report cursor", ErrReportInvalid)
	}
	if alegReportCursorPrefix+base64.RawURLEncoding.EncodeToString(canonical) != raw {
		return "", "", "", fmt.Errorf("%w: non-canonical A-leg report cursor", ErrReportInvalid)
	}
	return cursor.LegCall, cursor.LegBLeg, cursor.Call, nil
}
