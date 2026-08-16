package billing

import (
	"context"
	"errors"
)

var (
	ErrAccountConflict = errors.New("billing: account identity conflict")
	ErrAccountNotFound = errors.New("billing: account not found")
)

type AccountProvisioner interface {
	CreateAccount(context.Context, Account) error
	PostFunding(context.Context, FundingInput) (Posting, error)
	ChangeCreditPolicy(context.Context, CreditPolicyInput) (PolicyChange, error)
}
