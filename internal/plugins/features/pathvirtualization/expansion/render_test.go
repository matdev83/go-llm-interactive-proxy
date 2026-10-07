package expansion_test

// This file pins requirements.md 7.7 at the SERIALIZATION boundary of the expansion
// report: every enum dimension of the value must reach any exporter as a bounded
// string, never as the ordinal an integer-coded enum otherwise renders as.
//
// The expansion report carries THREE enum dimensions - the pass outcome, the per-call
// decision reason, and the lexical core's own root refusal - plus the shared engine's
// per-surface skip tally. A reader who found one of them rendering as a number would
// reasonably assume the rest do too, so all of them are pinned here rather than only
// the one the obligation happened to name. The out-of-vocabulary value is included for
// each, because it is the case a healthy build never produces and the one that must
// still degrade.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/expansion"
)

func TestEveryReportEnumRendersAsABoundedString(t *testing.T) {
	t.Parallel()
	for _, outcome := range []expansion.Outcome{
		expansion.OutcomeExpanded,
		expansion.OutcomeNoop,
		expansion.OutcomeRejected,
		expansion.Outcome(0xFF),
	} {
		assertEnumField(t, renderReport(t, outcome, expansion.ReasonExpanded), "Outcome", outcome.String())
	}
	for _, reason := range []expansion.Reason{
		expansion.ReasonNone,
		expansion.ReasonExpanded,
		expansion.ReasonAuditMode,
		expansion.ReasonNoAlias,
		expansion.ReasonNoSelectors,
		expansion.ReasonArgsAbsent,
		expansion.ReasonPayloadNotObject,
		expansion.ReasonRootUnusable,
		expansion.ReasonMappingInactive,
		expansion.ReasonArgsUnparseable,
		expansion.ReasonMalformedReservedAlias,
		expansion.ReasonWorkspaceMismatch,
		expansion.ReasonExpandedTooLarge,
		expansion.ReasonInvalidRewrite,
		expansion.Reason(0xFF),
	} {
		assertEnumField(t, renderReport(t, expansion.OutcomeNoop, reason), "Reason", reason.String())
	}
	for _, root := range []pathvirtualization.SkipReason{
		pathvirtualization.SkipReasonNone,
		pathvirtualization.SkipReasonEmptyRoot,
		pathvirtualization.SkipReasonRelativeRoot,
		pathvirtualization.SkipReasonMalformedVolumeRoot,
		pathvirtualization.SkipReasonDeviceNamespace,
		pathvirtualization.SkipReasonReservedNamespaceCollision,
		pathvirtualization.SkipReason("/home/dev/projects/go-llm-interactive-proxy"),
	} {
		assertEnumField(t, renderReportWithRoot(t, expansion.OutcomeNoop,
			expansion.ReasonRootUnusable, root), "RootReason", root.String())
	}
}

// TestTheRenderedReasonIsThePublishedReasonCode ties the two renderings together: the
// same label travels as the toolcall.Result.ReasonCode a client-facing refusal is built
// from, so a report and the refusal that names the same condition must not disagree
// about how to spell it.
func TestTheRenderedReasonIsThePublishedReasonCode(t *testing.T) {
	t.Parallel()
	rendered := renderReport(t, expansion.OutcomeRejected, expansion.ReasonWorkspaceMismatch)
	assertEnumField(t, rendered, "Reason", expansion.ReasonWorkspaceMismatch.String())
	if !strings.Contains(rendered, `"workspace_mismatch"`) {
		t.Fatalf("the stale-workspace label must reach the export verbatim: %s", rendered)
	}
}

func TestTheReportNeverRendersAnEnumAsAnOrdinal(t *testing.T) {
	t.Parallel()
	rendered := renderReport(t, expansion.Outcome(2), expansion.Reason(11))
	for _, ordinal := range []string{`"Outcome":2`, `"Outcome": 2`, `"Reason":11`, `"Reason": 11`} {
		if strings.Contains(rendered, ordinal) {
			t.Fatalf("an enum rendered as an ordinal, which is unbounded (requirements.md 7.7): %s", rendered)
		}
	}
}

func renderReport(t *testing.T, outcome expansion.Outcome, reason expansion.Reason) string {
	t.Helper()
	return renderReportWithRoot(t, outcome, reason, pathvirtualization.SkipReasonEmptyRoot)
}

func renderReportWithRoot(
	t *testing.T,
	outcome expansion.Outcome,
	reason expansion.Reason,
	root pathvirtualization.SkipReason,
) string {
	t.Helper()
	encoded, err := json.Marshal(expansion.Report{
		Outcome:    outcome,
		Reason:     reason,
		RootReason: root,
	})
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return string(encoded)
}

func assertEnumField(t *testing.T, rendered, field, want string) {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(rendered), &decoded); err != nil {
		t.Fatalf("decode rendered report %s: %v", rendered, err)
	}
	value, present := decoded[field]
	if !present {
		t.Fatalf("rendered report %s has no %s field", rendered, field)
	}
	text, isText := value.(string)
	if !isText {
		t.Fatalf("%s rendered as %T (%v), want the bounded string %q", field, value, value, want)
	}
	if text != want {
		t.Fatalf("%s rendered as %q, want %q", field, text, want)
	}
}
