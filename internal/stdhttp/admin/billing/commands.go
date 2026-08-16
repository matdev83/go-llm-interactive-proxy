package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

type commandRequest struct {
	AccountID       string `json:"account_id"`
	CallID          string `json:"call_id"`
	Currency        string `json:"currency"`
	Mode            string `json:"mode"`
	CreditLimitNano *int64 `json:"credit_limit_nano"`
	AmountNano      int64  `json:"amount_nano"`
	SourceKey       string `json:"source_key"`
	Reason          string `json:"reason"`
}

func fundingHandler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requirePOST(w, r) || !requireCommands(w, opts) {
			return
		}
		var req commandRequest
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
		var req commandRequest
		if !decodeCommandJSON(w, r, &req) {
			return
		}
		limit, err := creditLimit(req.CreditLimitNano, true)
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

func exposureRepairHandler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requirePOST(w, r) || !requireRecovery(w, opts) {
			return
		}
		var req commandRequest
		if !decodeCommandJSON(w, r, &req) {
			return
		}
		callID, err := corebilling.ParseBillingCallID(strings.TrimSpace(req.CallID))
		if err != nil {
			writeCommandError(w, fmt.Errorf("%w: %w", corebilling.ErrTrustedCommandInvalid, err))
			return
		}
		sourceKey := strings.TrimSpace(req.SourceKey)
		if sourceKey == "" {
			writeCommandError(w, fmt.Errorf("%w: source_key is required", corebilling.ErrTrustedCommandInvalid))
			return
		}
		mode := strings.TrimSpace(req.Mode)
		var settled corebilling.CallSettlement
		switch mode {
		case "complete":
			settled, err = opts.Recovery.RepairExposureNoCharge(r.Context(), callID, sourceKey)
		case "incomplete":
			settled, err = opts.Recovery.RepairIncompleteCallNoCharge(r.Context(), callID, sourceKey)
		default:
			writeCommandError(w, fmt.Errorf("%w: mode must be complete or incomplete", corebilling.ErrTrustedCommandInvalid))
			return
		}
		if err != nil {
			writeCommandError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settled)
	}
}

func handleCreateAccount(w http.ResponseWriter, r *http.Request, opts Options) {
	if !requireCommands(w, opts) {
		return
	}
	var req commandRequest
	if !decodeCommandJSON(w, r, &req) {
		return
	}
	mode := corebilling.AccountMode(strings.TrimSpace(req.Mode))
	limit, err := creditLimit(req.CreditLimitNano, mode == corebilling.AccountPostpaid)
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

func creditLimit(limit *int64, required bool) (int64, error) {
	if limit != nil {
		return *limit, nil
	}
	if required {
		return 0, fmt.Errorf("%w: credit limit is required", corebilling.ErrTrustedCommandInvalid)
	}
	return 0, nil
}

func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return false
	}
	return true
}

func requireCommands(w http.ResponseWriter, opts Options) bool {
	return requireService(w, opts.Commands != nil, "provisioner_unavailable")
}

func requireRecovery(w http.ResponseWriter, opts Options) bool {
	return requireService(w, opts.Recovery != nil, "recovery_unavailable")
}

func requireService(w http.ResponseWriter, available bool, code string) bool {
	if !available {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": code})
	}
	return available
}

func decodeCommandJSON(w http.ResponseWriter, r *http.Request, dest any) bool {
	if err := json.NewDecoder(r.Body).Decode(dest); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_command"})
		return false
	}
	return true
}

func writeCommandError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "billing_command_unavailable"
	switch {
	case errors.Is(err, corebilling.ErrTrustedCommandInvalid), errors.Is(err, corebilling.ErrAccountInvalid),
		errors.Is(err, corebilling.ErrMoneyInvalid), errors.Is(err, corebilling.ErrMoneyCurrencyMismatch),
		errors.Is(err, corebilling.ErrUnsafeCreditLimitReduction), errors.Is(err, corebilling.ErrSettlementInvalid),
		errors.Is(err, corebilling.ErrBillingCallIDInvalid):
		status, code = http.StatusBadRequest, "invalid_command"
	case errors.Is(err, corebilling.ErrAccountNotFound), errors.Is(err, corebilling.ErrExposureNotFound), errors.Is(err, corebilling.ErrCallIncomplete):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, corebilling.ErrAccountConflict), errors.Is(err, corebilling.ErrSettlementConflict), errors.Is(err, corebilling.ErrExposureConflict):
		status, code = http.StatusConflict, "conflict"
	}
	writeJSON(w, status, map[string]string{"error": code})
}
