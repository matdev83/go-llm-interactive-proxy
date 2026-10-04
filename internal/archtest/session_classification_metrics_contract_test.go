package archtest

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	featureclassification "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
)

// Task 11.2 (requirements 9.1-9.4, 9.6): the operator guide must describe the
// shipped observability surface truthfully. The metric names, exported types,
// label sets, emission points, and label vocabularies all come from source
// (see session_classification_metric_source_test.go); this file is the half that
// holds the prose to them.
//
// Every name/value pairing is required ON ONE LINE of the guide. Two independent
// strings.Contains calls would let a falsified label pass while the real one
// appears somewhere else in the document, which is exactly the stale
// documentation task 11.1's review caught.

// sessionClassificationObservabilitySection is the guide section that owns the
// metric surface.
const sessionClassificationObservabilitySection = "## Observability"

// sessionClassificationNeverALabel are the label sources requirement 9.4 forbids.
// The guide must name every one of them in the same "never a label" list, so an
// operator cannot read the shipped surface as permitting an open-ended value.
var sessionClassificationNeverALabel = []string{
	"SessionID",
	"ALegID",
	"raw `User-Agent`",
	"filename",
	"path",
	"prompt",
	"client metadata",
	"vendor result string",
}

// TestSessionClassificationDocs_documentEveryShippedMetricFamily proves the guide
// names every metric family the shipped collector exports, with the type it is
// actually exported as, the labels it actually carries, the emission point it
// actually uses, and every value of every closed label vocabulary on one line.
func TestSessionClassificationDocs_documentEveryShippedMetricFamily(t *testing.T) {
	t.Parallel()

	// Fill the two vocabularies that have no shipped accessor, from the SDK's own
	// declarations, before anything compares against them.
	transitions := sessionClassificationLabelVocabularies["counter:confidence,evidence,source"]
	if transitions["source"] == nil {
		transitions["source"] = sessionClassificationStringEnumValues(t, sessionSDKPackage, "ClassificationSource")
	}
	if transitions["confidence"] == nil {
		transitions["confidence"] = sessionClassificationStringEnumValues(t, sessionSDKPackage, "ConfidenceBand")
	}
	section := sessionClassificationGuideSection(t, sessionClassificationObservabilitySection)
	families := parseSessionClassificationMetrics(t)

	covered := map[string]bool{}
	for _, family := range families {
		signature := family.Signature()
		vocabulary, known := sessionClassificationLabelVocabularies[signature]
		if !known {
			t.Fatalf("%s exports family %q as %q, which no closed label vocabulary governs; the guide cannot "+
				"document a family whose labels are not in a closed enumeration",
				sessionClassificationCollectorRel, family.Name, signature)
		}
		if covered[signature] {
			t.Fatalf("two shipped metric families share the signature %q, so the guide cannot say which closed "+
				"vocabulary governs which", signature)
		}
		covered[signature] = true
		if len(family.Hooks) == 0 {
			t.Fatalf("shipped family %q has no collector method that feeds it, so the guide cannot claim an "+
				"emission point it cannot name", family.Name)
		}

		needles := append([]string{family.Name, family.Kind}, family.Hooks...)
		if len(family.Labels) == 0 {
			needles = append(needles, "no labels")
		}
		line := sessionClassificationGuideLine(t, section, sessionClassificationObservabilitySection,
			"row for metric "+family.Name, needles...)
		if len(family.Labels) == 0 {
			continue
		}
		for _, label := range family.Labels {
			if !strings.Contains(line, "`"+label+"`") {
				t.Fatalf("%s row for %q does not carry label %q; the shipped descriptor labels it %v",
					sessionClassificationDocRel, family.Name, label, family.Labels)
			}
			values, governed := vocabulary[label]
			if !governed {
				t.Fatalf("no closed vocabulary governs label %q of shipped family %q", label, family.Name)
			}
			for _, value := range values {
				sessionClassificationGuideLine(t, section, sessionClassificationObservabilitySection,
					"value "+value+" of label "+label+" on metric "+family.Name,
					family.Name, "`"+label+"`", "`"+value+"`")
			}
		}
	}

	for signature := range sessionClassificationLabelVocabularies {
		if !covered[signature] {
			t.Errorf("no shipped metric family has signature %q, so the expected closed-vocabulary table is "+
				"stale and the guide documents a surface that does not ship", signature)
		}
	}
}

// TestSessionClassificationDocs_neverALabelListIsClosed proves requirement 9.4 is
// stated as a closed prohibition in one place next to the labels that honour it,
// and that the two values the shipped code deliberately keeps out of the label
// set are called out with their shipped bounds attached.
func TestSessionClassificationDocs_neverALabelListIsClosed(t *testing.T) {
	t.Parallel()
	section := sessionClassificationGuideSection(t, sessionClassificationObservabilitySection)

	block := sessionClassificationGuideBulletBlock(t, section, sessionClassificationObservabilitySection,
		"never a label")
	for _, forbidden := range sessionClassificationNeverALabel {
		if !strings.Contains(block, forbidden) {
			t.Errorf("%s %q does not forbid %q as a label; requirement 9.4 forbids session IDs, A-leg IDs, "+
				"raw User-Agent, filenames, paths, prompts, arbitrary client metadata, and arbitrary vendor "+
				"result strings", sessionClassificationDocRel, sessionClassificationObservabilitySection, forbidden)
		}
	}

	// The revision is deliberately absent from the label set because it is
	// unbounded, and the guide must say so with the shipped bound attached rather
	// than gesturing at the problem.
	revision := strconv.FormatUint(featureclassification.MaxObservedRevision, 10)
	if !regexp.MustCompile("`MaxObservedRevision`[^|\\n]*\\|[^|\\n]*" + regexp.QuoteMeta(revision)).
		MatchString(section) {
		t.Errorf("%s %q must attach the shipped MaxObservedRevision (%s) to the explanation that a revision is "+
			"unbounded and therefore never a label",
			sessionClassificationDocRel, sessionClassificationObservabilitySection, revision)
	}

	// The latency histogram is bounded by dropping an out-of-range sample rather
	// than truncating it, so the guide must state the shipped sample ceiling.
	latency := operatorDuration(featureclassification.MaxRemoteObservationLatency)
	if !strings.Contains(section, "`MaxRemoteObservationLatency`") || !strings.Contains(section, latency) {
		t.Errorf("%s %q must state the shipped MaxRemoteObservationLatency bound (%s) that keeps one latency "+
			"sample inside a fixed range", sessionClassificationDocRel, sessionClassificationObservabilitySection, latency)
	}
}
