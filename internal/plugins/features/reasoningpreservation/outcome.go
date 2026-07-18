package reasoningpreservation

type SafeOutcome string

const (
	OutcomeObserved        SafeOutcome = "observed"
	OutcomePreserved       SafeOutcome = "preserved"
	OutcomeMissing         SafeOutcome = "missing"
	OutcomeRestored        SafeOutcome = "restored"
	OutcomeAmbiguous       SafeOutcome = "ambiguous"
	OutcomeConflicting     SafeOutcome = "conflicting"
	OutcomeUnmatched       SafeOutcome = "unmatched"
	OutcomeUnrepresentable SafeOutcome = "unrepresentable"
	OutcomeStateError      SafeOutcome = "state_error"
	OutcomeEvicted         SafeOutcome = "evicted"
	OutcomeOversize        SafeOutcome = "oversize"
)

func FormatSafeDiagnostic(outcome SafeOutcome, ruleID string, counts map[string]int) (string, error) {
	_ = outcome
	_ = ruleID
	_ = counts
	return "", ErrNotImplemented
}

func ProjectSafeError(err error) (string, error) {
	_ = err
	return "", ErrNotImplemented
}
