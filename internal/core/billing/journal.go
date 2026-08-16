package billing

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrJournalInvalid     = errors.New("billing: invalid journal transaction")
	ErrJournalUnbalanced  = errors.New("billing: unbalanced journal transaction")
	ErrJournalFingerprint = errors.New("billing: journal fingerprint mismatch")
)

const JournalFingerprintPrefix = "journal-fp:v2:"

type JournalBook string

const (
	JournalBookFinancial           JournalBook = "financial"
	JournalBookLegacyAuthorization JournalBook = "authorization"
)

type JournalSide string

const (
	JournalDebit  JournalSide = "debit"
	JournalCredit JournalSide = "credit"
)

type JournalEntry struct {
	LedgerAccount string
	Side          JournalSide
	Amount        Money
}
type JournalTransaction struct {
	ID                    string
	Book                  JournalBook
	Currency              string
	SourceKey             string
	SemanticFingerprint   string
	AccountID             string
	TurnID                string
	ALegID                string
	BLegID                string
	AccountSequence       uint64
	ReversalOf            string
	CorrectsTransactionID string
	CorrectionGroupID     string
	OperationKind         string
	BalanceBefore         int64
	BalanceAfter          int64
	ReservedBefore        int64
	ReservedAfter         int64
	SpendableBefore       int64
	SpendableAfter        int64
	CreditFloor           int64
	CreditLimit           int64
	Mode                  string
	SnapshotVersionBefore uint64
	SnapshotVersionAfter  uint64
	Entries               []JournalEntry
}

func (j JournalTransaction) Validate() error {
	if strings.TrimSpace(j.ID) == "" || strings.TrimSpace(j.SourceKey) == "" {
		return fmt.Errorf("%w: id and source key are required", ErrJournalInvalid)
	}
	if j.Book != JournalBookFinancial && j.Book != JournalBookLegacyAuthorization {
		return fmt.Errorf("%w: unsupported book %q", ErrJournalInvalid, j.Book)
	}
	if strings.TrimSpace(j.Currency) == "" {
		return fmt.Errorf("%w: currency is required", ErrJournalInvalid)
	}
	if len(j.Entries) < 2 {
		return fmt.Errorf("%w: at least two entries are required", ErrJournalInvalid)
	}
	var debits, credits int64
	for _, entry := range j.Entries {
		if strings.TrimSpace(entry.LedgerAccount) == "" || (entry.Side != JournalDebit && entry.Side != JournalCredit) {
			return fmt.Errorf("%w: entry account and side are required", ErrJournalInvalid)
		}
		if entry.Amount.Currency != j.Currency || entry.Amount.Nano <= 0 {
			return fmt.Errorf("%w: entry amount must be positive and use transaction currency", ErrJournalInvalid)
		}
		var err error
		if entry.Side == JournalDebit {
			debits, err = checkedAdd(debits, entry.Amount.Nano)
		} else {
			credits, err = checkedAdd(credits, entry.Amount.Nano)
		}
		if err != nil {
			return err
		}
	}
	if debits != credits {
		return fmt.Errorf("%w: debits=%d credits=%d", ErrJournalUnbalanced, debits, credits)
	}
	if j.ReversalOf != "" && j.ReversalOf == j.ID {
		return fmt.Errorf("%w: transaction cannot reverse itself", ErrJournalInvalid)
	}
	if j.CorrectsTransactionID != "" && j.CorrectsTransactionID == j.ID {
		return fmt.Errorf("%w: transaction cannot correct itself", ErrJournalInvalid)
	}
	return nil
}

func (j JournalTransaction) CanonicalFingerprint() (string, error) {
	if err := j.Validate(); err != nil {
		return "", err
	}
	canonical := struct {
		Version               string
		Book                  JournalBook
		Currency              string
		SourceKey             string
		AccountID             string
		TurnID                string
		ALegID                string
		BLegID                string
		ReversalOf            string
		CorrectsTransactionID string
		CorrectionGroupID     string
		OperationKind         string
		Entries               []JournalEntry
	}{
		Version: "journal-fp:v2",
		Book:    j.Book, Currency: j.Currency, SourceKey: j.SourceKey,
		AccountID: j.AccountID, TurnID: j.TurnID, ALegID: j.ALegID, BLegID: j.BLegID,
		ReversalOf: j.ReversalOf, CorrectsTransactionID: j.CorrectsTransactionID,
		CorrectionGroupID: j.CorrectionGroupID, OperationKind: j.OperationKind,
		Entries: j.Entries,
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return JournalFingerprintPrefix + fmt.Sprintf("%x", digest[:]), nil
}

func (j JournalTransaction) Seal() (JournalTransaction, error) {
	fp, err := j.CanonicalFingerprint()
	if err != nil {
		return JournalTransaction{}, err
	}
	out, err := j.Detached()
	if err != nil {
		return JournalTransaction{}, err
	}
	out.SemanticFingerprint = fp
	return out, nil
}

func (j JournalTransaction) Detached() (JournalTransaction, error) {
	if err := j.Validate(); err != nil {
		return JournalTransaction{}, err
	}
	out := j
	out.Entries = append([]JournalEntry(nil), j.Entries...)
	return out, nil
}
