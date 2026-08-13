package billing

import (
	"context"
	"errors"
)

var (
	// ErrAccountConflict is a trusted-command identity collision (duplicate
	// account or conflicting source key). HTTP maps it to 409.
	ErrAccountConflict = errors.New("billing: account identity conflict")
	// ErrAccountNotFound is a trusted command against a missing account.
	// HTTP maps it to 404. Distinct from ErrAccountNotReady.
	ErrAccountNotFound = errors.New("billing: account not found")
)

// AccountProvisioner is the trusted operator command seam for account create,
// funding, and credit-policy changes already implemented by the durable store.
// It is not part of AuthoritativeBilling; admission-only fakes must not be
// required to implement it. HTTP and tests depend on this port and must not
// import the store package. PostPayment and PostAdjustment are out of scope.
type AccountProvisioner interface {
	CreateAccount(context.Context, Account) error
	PostFunding(context.Context, FundingInput) (Posting, error)
	ChangeCreditPolicy(context.Context, CreditPolicyInput) (PolicyChange, error)
}
