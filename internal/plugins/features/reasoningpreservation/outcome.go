package reasoningpreservation

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

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

var safeCountKeys = map[string]struct{}{
	"count":           {},
	"bytes":           {},
	"restored":        {},
	"observed":        {},
	"preserved":       {},
	"missing":         {},
	"ambiguous":       {},
	"conflicting":     {},
	"unmatched":       {},
	"unrepresentable": {},
	"state_error":     {},
	"evicted":         {},
	"oversize":        {},
}

func FormatSafeDiagnostic(outcome SafeOutcome, ruleID string, counts map[string]int) (string, error) {
	if !isKnownOutcome(outcome) {
		return "", fmt.Errorf("%s: unknown outcome", ID)
	}
	safeRule := sanitizeRuleID(ruleID)
	parts := []string{"outcome=" + string(outcome)}
	if safeRule != "" {
		parts = append(parts, "rule="+safeRule)
	}
	if len(counts) > 0 {
		keys := make([]string, 0, len(counts))
		for k := range counts {
			if _, ok := safeCountKeys[k]; ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
		}
	}
	return strings.Join(parts, " "), nil
}

func ProjectSafeError(err error) (string, error) {
	if err == nil {
		return "", fmt.Errorf("%s: nil error", ID)
	}
	return ID + ": operation failed", nil
}

func isKnownOutcome(o SafeOutcome) bool {
	switch o {
	case OutcomeObserved, OutcomePreserved, OutcomeMissing, OutcomeRestored,
		OutcomeAmbiguous, OutcomeConflicting, OutcomeUnmatched,
		OutcomeUnrepresentable, OutcomeStateError, OutcomeEvicted, OutcomeOversize:
		return true
	default:
		return false
	}
}

func sanitizeRuleID(ruleID string) string {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return ""
	}
	if i := strings.IndexByte(ruleID, '/'); i >= 0 {
		ruleID = ruleID[:i]
	}
	if !isSafeLabel(ruleID) {
		return ""
	}
	return ruleID
}

func isSafeLabel(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
