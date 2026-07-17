package secretguard

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	decisionFailureKindScanLimit            = "scan_limit"
	decisionFailureKindUnsupportedJSONToken = "unsupported_json_token"

	decisionMaxTokenBytes    = 128
	decisionMaxReasonBytes   = 256
	findingMaxSecretRefBytes = 128
	findingMaxLocationBytes  = 256
	findingMaxAliasBytes     = 128
	findingMaxAliases        = 8
)

// Validate reports whether the decision shape is safe for runner acceptance.
//
// It checks only bounded, secret-safe metadata. The method never inspects raw
// secret contents and returns generic field errors only.
func (d Decision) Validate() error {
	if !isKnownOutcome(d.Outcome) {
		return invalidDecisionField("outcome")
	}
	if d.MutationCount < 0 {
		return invalidDecisionField("mutation_count")
	}
	switch d.Outcome {
	case OutcomePass:
		if d.MutationCount != 0 {
			return invalidDecisionField("mutation_count")
		}
		if len(d.Findings) != 0 || d.ScanLimitHit || d.FailureKind != "" || d.FailureReason != "" {
			return invalidDecisionField("pass_shape")
		}
	case OutcomeLog:
		if d.MutationCount != 0 {
			return invalidDecisionField("mutation_count")
		}
		if d.ScanLimitHit {
			if err := validateScanLimitMetadata(d.FailureKind, d.FailureReason); err != nil {
				return err
			}
		} else if len(d.Findings) == 0 || d.FailureKind != "" || d.FailureReason != "" {
			return invalidDecisionField("log_shape")
		}
	case OutcomeRedacted:
		if d.MutationCount <= 0 {
			return invalidDecisionField("mutation_count")
		}
		if len(d.Findings) == 0 {
			return invalidDecisionField("redacted_shape")
		}
		if d.ScanLimitHit || d.FailureKind != "" || d.FailureReason != "" {
			return invalidDecisionField("redacted_shape")
		}
	case OutcomeBlock:
		if d.MutationCount != 0 {
			return invalidDecisionField("mutation_count")
		}
		if d.ScanLimitHit {
			if err := validateScanLimitMetadata(d.FailureKind, d.FailureReason); err != nil {
				return err
			}
		} else if d.FailureKind == "" {
			// Plain block decisions remain legal with or without findings.
		} else if d.FailureKind == decisionFailureKindUnsupportedJSONToken {
			if len(d.Findings) == 0 {
				return invalidDecisionField("block_shape")
			}
			if err := validateUnsupportedJSONTokenMetadata(d.FailureReason); err != nil {
				return err
			}
		} else {
			return invalidDecisionField("failure_kind")
		}
	}

	for i := range d.Findings {
		if err := d.Findings[i].Validate(); err != nil {
			return fmt.Errorf("secretguard.Decision: invalid finding[%d].%s", i, errField(err))
		}
	}
	return nil
}

// Validate reports whether the finding shape is safe for runner acceptance.
func (f Finding) Validate() error {
	if err := validateSafeText("secret_ref_name", f.SecretRefName, findingMaxSecretRefBytes, true); err != nil {
		return err
	}
	if len(f.Aliases) > findingMaxAliases {
		return invalidDecisionField("aliases")
	}
	for i := range f.Aliases {
		if err := validateSafeText(fmt.Sprintf("aliases[%d]", i), f.Aliases[i], findingMaxAliasBytes, true); err != nil {
			return err
		}
	}
	if err := validateSourceCategory(f.SourceCategory); err != nil {
		return err
	}
	if err := validateSafeText("location", f.Location, findingMaxLocationBytes, false); err != nil {
		return err
	}
	if f.OccurrenceCount <= 0 {
		return invalidDecisionField("occurrence_count")
	}
	return nil
}

func validateScanLimitMetadata(kind, reason string) error {
	if kind != decisionFailureKindScanLimit {
		return invalidDecisionField("failure_kind")
	}
	if err := validateSafeText("failure_kind", kind, decisionMaxTokenBytes, true); err != nil {
		return err
	}
	if err := validateSafeText("failure_reason", reason, decisionMaxReasonBytes, true); err != nil {
		return err
	}
	return nil
}

func validateUnsupportedJSONTokenMetadata(reason string) error {
	if err := validateSafeText("failure_reason", reason, decisionMaxReasonBytes, true); err != nil {
		return err
	}
	return nil
}

func validateSourceCategory(cat SourceCategory) error {
	switch cat {
	case "", SourceCategoryProxyEnv, SourceCategoryPopularEnv, SourceCategoryOperatorEnv, SourceCategoryRequestCred, SourceCategoryUnknown:
		return nil
	default:
		return invalidDecisionField("source_category")
	}
}

func validateSafeText(field, s string, maxBytes int, required bool) error {
	if required && strings.TrimSpace(s) == "" {
		return invalidDecisionField(field)
	}
	if len(s) > maxBytes {
		return invalidDecisionField(field)
	}
	if !utf8.ValidString(s) {
		return invalidDecisionField(field)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return invalidDecisionField(field)
		}
	}
	return nil
}

func invalidDecisionField(field string) error {
	return fmt.Errorf("secretguard.Decision: invalid %s", field)
}

func isKnownOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomePass, OutcomeLog, OutcomeRedacted, OutcomeBlock:
		return true
	default:
		return false
	}
}

func errField(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, "invalid "); i >= 0 {
		return msg[i+len("invalid "):]
	}
	return "field"
}
