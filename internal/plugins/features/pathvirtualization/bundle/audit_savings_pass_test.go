package bundle_test

// Spec: b-leg-path-virtualization Task 12.1, requirements.md 7.3, 7.6, 7.7, 9.1 and 9.5.
//
// This file is the OPERATOR-FACING statement of the pass-breakdown fix, driven through the
// SHIPPED composition seam exactly as the sibling audit suite does (config.Decode ->
// bundle.FeatureBundleWithTelemetry -> telemetry.Snapshot). A fix that existed only in the
// telemetry package would leave the wiring free to drop the dimension, and the wiring is
// where the defect actually reached an operator.
//
// The defect: with the shipped two-pass composition, an AUDIT deployment's generation-wide
// saving was exactly TWICE the rewrite deployment's for identical traffic, because the
// rewrite publishes the alias and the late pass then measures nothing, while the audit
// publishes nothing and the late pass measures the same figure again. Requirement 9.5 asks
// for the realized per-candidate saving to be CALCULABLE, and a doubled total with no way
// to see why is not calculable.
//
// What is pinned here:
//
//	THE GENERATION TOTAL IS NOW ASSERTED. The sibling suite deliberately left it
//	    unasserted because no correct answer had been decided. There is one now, and it is
//	    the arithmetic the two modes actually produce: a rewrite reports one pass's worth
//	    and an audit reports two.
//	THE PER-CANDIDATE FIGURE IS RECOVERABLE AND MODE-INDEPENDENT. The early pass's row is
//	    the same figure in both modes, and it is exactly what the rewrite deployment
//	    publishes as its total. That is the number requirement 9.5 asks an operator to be
//	    able to calculate, and it is now readable rather than inferred.
//	CARDINALITY IS BOUNDED AND CONTENT-FREE. The rows are a fixed-size series over a
//	    closed label set, and the hostile private fixture's tool name, pointer, root,
//	    alias, and tag appear in none of them.
//
// NOTHING HERE DEPENDS ON A HOST PATH OR A WALL CLOCK. Every figure is a count or a byte
// total derived from the mapper's own decision, and every failure message is bounded.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/outbound"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/telemetry"
)

// savingsPassLabels is the closed label set a published pass row may carry.
var savingsPassLabels = map[string]bool{
	"unattributed": true,
	"attempt":      true,
	"request_part": true,
	"unknown":      true,
}

// savingsPassRow returns the published row for one pass label.
func savingsPassRow(s telemetry.Snapshot, label string) (telemetry.PassCounters, bool) {
	for _, row := range s.Outbound.ByPass {
		if row.Pass == label {
			return row, true
		}
	}
	return telemetry.PassCounters{}, false
}

// savingsPassRowsSum returns the saving and eligible totals the rows account for.
func savingsPassRowsSum(s telemetry.Snapshot) (saved, eligible int64) {
	for _, row := range s.Outbound.ByPass {
		saved += row.BytesSaved
		eligible += row.Eligible
	}
	return saved, eligible
}

// TestThePerCandidateSavingIsRecoverableFromThePassRows is requirement 9.5 at the seam an
// operator reads.
//
// The fixture drives the PRODUCTION ordering - one shared candidate seen by both passes -
// because that is the only shape in which the two modes differ at all. Every occurrence tier
// and flavor is run so the recovery rule cannot hold for one shape only.
func TestThePerCandidateSavingIsRecoverableFromThePassRows(t *testing.T) {
	t.Parallel()
	for _, flavor := range auditSavingsFlavors {
		for _, occurrences := range auditSavingsOccurrenceTiers {
			name := flavor.name + "_occurrences_" + itoa(occurrences) + "_shared_candidate"
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				tc := auditSavingsCase{
					name: name, flavor: flavor, occurrences: occurrences, eligible: true,
				}
				before, after := tc.auditSavingsExpectation(t)
				perCandidate := (before - after) * int64(occurrences)
				if perCandidate <= 0 {
					t.Fatal("the fixture measured no saving; the assertions below would be vacuous")
				}

				audit := tc.run(t, rewrite.ModeAudit, true)
				mutated := tc.run(t, rewrite.ModeRewrite, true)

				// A REWRITE realizes one pass's worth: the late pass finds the alias the
				// early pass published and measures nothing (requirement 2.9).
				mutatedEarly, ok := savingsPassRow(mutated.final, outbound.PassAttempt.String())
				if !ok {
					t.Fatalf("%s: the rewrite deployment published no attempt row", name)
				}
				if mutatedEarly.BytesSaved != perCandidate {
					t.Errorf("%s: the rewrite attempt row saved %d, want %d",
						name, mutatedEarly.BytesSaved, perCandidate)
				}
				if mutatedEarly.Eligible != int64(occurrences) {
					t.Errorf("%s: the rewrite attempt row measured %d eligible occurrences, want %d",
						name, mutatedEarly.Eligible, occurrences)
				}
				// The late pass ran and found nothing. It keeps its row - a non-zero report
				// count with zero figures - because "it ran and found nothing" and "it did
				// not run" are different operational facts, and only the second is an absent
				// row.
				late, present := savingsPassRow(mutated.final, outbound.PassRequestPart.String())
				if !present {
					t.Errorf("%s: the rewrite deployment published no late-pass row, so a pass that "+
						"never ran is indistinguishable from one that found nothing", name)
				} else if late.Reports != 1 || late.Eligible != 0 || late.Rewritten != 0 ||
					late.BytesBefore != 0 || late.BytesAfter != 0 || late.BytesSaved != 0 {
					t.Errorf("%s: the rewrite late-pass row = %+v, want one report and no measurement",
						name, late)
				}
				if got := mutated.final.Total.BytesSaved; got != perCandidate {
					t.Errorf("%s: the rewrite generation saving = %d, want %d", name, got, perCandidate)
				}

				// An AUDIT measures the same unmutated candidate twice, and now says so:
				// two rows, each one pass's worth.
				auditEarly, ok := savingsPassRow(audit.final, outbound.PassAttempt.String())
				if !ok {
					t.Fatalf("%s: the audit deployment published no attempt row", name)
				}
				auditLate, ok := savingsPassRow(audit.final, outbound.PassRequestPart.String())
				if !ok {
					t.Fatalf("%s: the audit deployment published no request-part row", name)
				}
				if auditEarly.BytesSaved != perCandidate || auditLate.BytesSaved != perCandidate {
					t.Errorf("%s: the audit rows saved %d and %d, want %d each",
						name, auditEarly.BytesSaved, auditLate.BytesSaved, perCandidate)
				}
				if got, want := audit.final.Total.BytesSaved, 2*perCandidate; got != want {
					t.Errorf("%s: the audit generation saving = %d, want %d: the late pass "+
						"re-measured the same unmutated candidate", name, got, want)
				}

				// The requirement itself: the per-candidate figure is the attempt row, and
				// it is the SAME number in both modes and equal to what a rewrite
				// deployment publishes as its whole total.
				if auditEarly.BytesSaved != mutatedEarly.BytesSaved {
					t.Errorf("%s: the recoverable per-candidate saving differs by mode: audit %d, rewrite %d",
						name, auditEarly.BytesSaved, mutatedEarly.BytesSaved)
				}
				if auditEarly.BytesSaved != mutated.final.Total.BytesSaved {
					t.Errorf("%s: the audit deployment's recoverable figure %d is not the rewrite deployment's total %d",
						name, auditEarly.BytesSaved, mutated.final.Total.BytesSaved)
				}

				// The breakdown partitions the same measurement rather than restating it.
				for label, snapshot := range map[string]telemetry.Snapshot{"audit": audit.final, "rewrite": mutated.final} {
					saved, eligible := savingsPassRowsSum(snapshot)
					if saved != snapshot.Total.BytesSaved {
						t.Errorf("%s/%s: the rows sum to %d but the total is %d",
							name, label, saved, snapshot.Total.BytesSaved)
					}
					if eligible != snapshot.Outbound.Virtualized.Eligible {
						t.Errorf("%s/%s: the rows account for %d eligible occurrences but the tally is %d",
							name, label, eligible, snapshot.Outbound.Virtualized.Eligible)
					}
				}
			})
		}
	}
}

// TestThePassRowsAreBoundedAndCarryNoContent is requirements 7.7 and 9.3 on the new
// dimension, driven by the hostile private fixture so the scan is over rows a
// fully-configured deployment really publishes.
func TestThePassRowsAreBoundedAndCarryNoContent(t *testing.T) {
	t.Parallel()
	for _, mode := range []rewrite.Mode{rewrite.ModeAudit, rewrite.ModeRewrite} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			tel, b := auditSavingsPrivateBundle(t, mode)
			drivePrivateSavings(t, tel, b)

			snapshot := tel.Snapshot()
			// Non-vacuity: both passes ran and measured something, so the rows below are
			// published figures rather than an empty series.
			if len(snapshot.Outbound.ByPass) < 2 {
				t.Fatalf("the hostile fixture published %d pass rows, want at least 2: %s",
					len(snapshot.Outbound.ByPass), mustMarshal(t, snapshot.Outbound.ByPass))
			}
			if snapshot.Outbound.Virtualized.BytesSaved <= 0 {
				t.Fatalf("the hostile fixture recorded no outbound saving, so the rows prove nothing")
			}
			if len(snapshot.Outbound.ByPass) > len(savingsPassLabels) {
				t.Errorf("the series holds %d rows, over the closed set's %d: cardinality is not bounded",
					len(snapshot.Outbound.ByPass), len(savingsPassLabels))
			}
			for _, row := range snapshot.Outbound.ByPass {
				if !savingsPassLabels[row.Pass] {
					t.Errorf("row label %q is outside the closed set", row.Pass)
				}
			}

			rendered := mustMarshal(t, snapshot.Outbound.ByPass)
			for _, forbidden := range []string{
				auditSavingsPrivateTool,
				auditSavingsPrivatePointer,
				auditSavingsPrivateRoot,
				auditSavingsStaleAlias,
				auditSavingsPrivateCallID,
				".__lip_v1__",
				"w_",
				"pack/lipapi",
			} {
				if strings.Contains(rendered, forbidden) {
					t.Errorf("the rendered pass rows carry %q: %s", forbidden, rendered)
				}
			}
		})
	}
}

// itoa is the bounded integer spelling the subtest names use, so no case name carries a
// payload byte.
func itoa(value int) string { return strconv.Itoa(value) }
