package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

type createAccountRequest struct {
	AccountID       string `json:"account_id"`
	Currency        string `json:"currency"`
	Mode            string `json:"mode"`
	CreditLimitNano *int64 `json:"credit_limit_nano"`
}

type fundingRequest struct {
	AccountID  string `json:"account_id"`
	AmountNano int64  `json:"amount_nano"`
	Currency   string `json:"currency"`
	SourceKey  string `json:"source_key"`
	Reason     string `json:"reason"`
}

type creditPolicyRequest struct {
	AccountID       string `json:"account_id"`
	Mode            string `json:"mode"`
	Currency        string `json:"currency"`
	CreditLimitNano *int64 `json:"credit_limit_nano"`
	SourceKey       string `json:"source_key"`
	Reason          string `json:"reason"`
}

func fundingHandler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requirePOST(w, r) || !requireCommands(w, opts) {
			return
		}
		var req fundingRequest
		if !decodeCommandJSON(w, r, &req) {
			return
		}
		input := corebilling.FundingInput{
			AccountID: strings.TrimSpace(req.AccountID),
			Amount:    corebilling.Money{Nano: req.AmountNano, Currency: strings.TrimSpace(req.Currency)},
			SourceKey: strings.TrimSpace(req.SourceKey),
			Reason:    strings.TrimSpace(req.Reason),
		}
		if err := input.Validate(); err != nil {
			writeCommandError(w, err)
			return
		}
		posting, err := opts.Commands.PostFunding(r.Context(), input)
		if err != nil {
			writeCommandError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, posting)
	}
}

func creditPolicyHandler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requirePOST(w, r) || !requireCommands(w, opts) {
			return
		}
		var req creditPolicyRequest
		if !decodeCommandJSON(w, r, &req) {
			return
		}
		limit, err := requireCreditLimit(req.CreditLimitNano)
		if err != nil {
			writeCommandError(w, err)
			return
		}
		input := corebilling.CreditPolicyInput{
			AccountID:   strings.TrimSpace(req.AccountID),
			Mode:        corebilling.AccountMode(strings.TrimSpace(req.Mode)),
			Currency:    strings.TrimSpace(req.Currency),
			CreditLimit: limit,
			SourceKey:   strings.TrimSpace(req.SourceKey),
			Reason:      strings.TrimSpace(req.Reason),
		}
		if err := input.Validate(); err != nil {
			writeCommandError(w, err)
			return
		}
		change, err := opts.Commands.ChangeCreditPolicy(r.Context(), input)
		if err != nil {
			writeCommandError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, change)
	}
}

func handleCreateAccount(w http.ResponseWriter, r *http.Request, opts Options) {
	if !requireCommands(w, opts) {
		return
	}
	var req createAccountRequest
	if !decodeCommandJSON(w, r, &req) {
		return
	}
	mode := corebilling.AccountMode(strings.TrimSpace(req.Mode))
	limit, err := creditLimitForCreate(mode, req.CreditLimitNano)
	if err != nil {
		writeCommandError(w, err)
		return
	}
	account := corebilling.Account{
		ID:          strings.TrimSpace(req.AccountID),
		Currency:    strings.TrimSpace(req.Currency),
		Mode:        mode,
		CreditLimit: limit,
		State:       corebilling.AccountReady,
		Version:     1,
	}
	if err := account.Validate(); err != nil {
		writeCommandError(w, err)
		return
	}
	if err := opts.Commands.CreateAccount(r.Context(), account); err != nil {
		writeCommandError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"account_id": account.ID})
}

func creditLimitForCreate(mode corebilling.AccountMode, limit *int64) (int64, error) {
	if mode == corebilling.AccountPostpaid && limit == nil {
		return 0, fmt.Errorf("%w: postpaid credit limit is required", corebilling.ErrTrustedCommandInvalid)
	}
	if limit == nil {
		return 0, nil
	}
	return *limit, nil
}

func requireCreditLimit(limit *int64) (int64, error) {
	if limit == nil {
		return 0, fmt.Errorf("%w: credit limit is required", corebilling.ErrTrustedCommandInvalid)
	}
	return *limit, nil
}

func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return false
	}
	return true
}

func requireCommands(w http.ResponseWriter, opts Options) bool {
	if opts.Commands == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "provisioner_unavailable"})
		return false
	}
	return true
}

func decodeCommandJSON(w http.ResponseWriter, r *http.Request, dest any) bool {
	if err := json.NewDecoder(r.Body).Decode(dest); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_command"})
		return false
	}
	return true
}

func writeCommandError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, corebilling.ErrTrustedCommandInvalid),
		errors.Is(err, corebilling.ErrAccountInvalid),
		errors.Is(err, corebilling.ErrMoneyInvalid),
		errors.Is(err, corebilling.ErrMoneyCurrencyMismatch),
		errors.Is(err, corebilling.ErrUnsafeCreditLimitReduction):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_command"})
	case errors.Is(err, corebilling.ErrAccountNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	case errors.Is(err, corebilling.ErrAccountConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "billing_command_unavailable"})
	}
}
