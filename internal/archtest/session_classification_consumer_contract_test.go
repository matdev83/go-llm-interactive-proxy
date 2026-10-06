package archtest

import (
	"strings"
	"testing"
)

// Task 11.2 (requirements 1.6, 1.8, 9.5, 11.3-11.6, 11.7): the reason this
// infrastructure exists is that a later feature reads one projected fact instead
// of writing a second client detector. The guide has to say that in terms a
// plugin author can act on, and the correct/incorrect pair below is what makes
// the difference checkable rather than aspirational.

// sessionClassificationConsumerSection is the guide section a future feature
// author implements against.
const sessionClassificationConsumerSection = "## Consuming classification"

// sessionClassificationReimplementationTokens are the techniques a consumer must
// not use once the projection exists, folded to lowercase alphanumerics. The
// correct example may contain none of them; the incorrect example must contain at
// least one.
//
// Folding case and separators is not cosmetic: Go spells the field `ClientUserAgent`
// and prose spells it `User-Agent`, so a literal token check for either spelling
// would silently miss the other. That is a bug this contract once had, caught by
// a mutation proof rather than by reading the test.
var sessionClassificationReimplementationTokens = []string{
	"useragent",
	"classifytoolname",
	"classify",
	"transcript",
}

// sessionClassificationFold reduces text to lowercase alphanumerics so a token
// check cannot miss an identifier or a prose spelling of the same technique.
func sessionClassificationFold(text string) string {
	folded := make([]rune, 0, len(text))
	for _, symbol := range text {
		switch {
		case symbol >= 'A' && symbol <= 'Z':
			folded = append(folded, symbol+('a'-'A'))
		case (symbol >= 'a' && symbol <= 'z') || (symbol >= '0' && symbol <= '9'):
			folded = append(folded, symbol)
		}
	}
	return string(folded)
}

// TestSessionClassificationDocs_consumerContractNamesTheProjectedFact proves the
// consumer contract is written against the shipped projection rather than
// against the feature's internals.
func TestSessionClassificationDocs_consumerContractNamesTheProjectedFact(t *testing.T) {
	t.Parallel()
	section := sessionClassificationGuideSection(t, sessionClassificationConsumerSection)

	// The accessor, the field, and the fact itself must appear together, so a
	// reader cannot copy the field name without the call that validates it.
	sessionClassificationGuideLine(t, section, sessionClassificationConsumerSection,
		"projected-fact statement", "SessionView", "Classification", "IsCodingAgent()")

	// Every technique the projection replaces must be named as prohibited.
	for _, banned := range []string{
		"re-run the classifier",
		"parse the client `User-Agent`",
		"rescan the transcript",
		"feature-private",
		"vendor-specific result",
	} {
		if !strings.Contains(section, banned) {
			t.Errorf("%s %s does not prohibit %q; requirement 1.6 requires a consumer to decide from the "+
				"immutable snapshot alone", sessionClassificationDocRel, sessionClassificationConsumerSection, banned)
		}
	}

	// Advisory, not authority, and no automatic gating: both are stated in this
	// section rather than only in the scope section, because this is the section
	// an author copies from.
	for _, marker := range []string{
		"advisory",
		"not authorization",
		"do not become gated automatically",
	} {
		if !strings.Contains(section, marker) {
			t.Errorf("%s %s does not state %q; a consumer must learn that classification is advisory and "+
				"that no existing feature becomes gated by it (requirements 1.7, 1.8)",
				sessionClassificationDocRel, sessionClassificationConsumerSection, marker)
		}
	}
}

// TestSessionClassificationDocs_correctExampleOnlyChecksTheProjection is the
// adjacency-with-content half of the contract: the example marked correct must
// actually consult only the projection, with no reimplementation token anywhere
// in it. A correct-looking example that quietly parses the User-Agent is the
// failure mode this catches.
func TestSessionClassificationDocs_correctExampleOnlyChecksTheProjection(t *testing.T) {
	t.Parallel()
	section := sessionClassificationGuideSection(t, sessionClassificationConsumerSection)

	body := sessionClassificationGuideFenceLanguage(t, section, sessionClassificationConsumerSection, "go", 1)
	if !strings.Contains(body, "IsCodingAgent()") {
		t.Errorf("%s %s first example does not call IsCodingAgent(); the correct consumer reads the projected "+
			"fact and nothing else", sessionClassificationDocRel, sessionClassificationConsumerSection)
	}
	folded := sessionClassificationFold(body)
	for _, banned := range sessionClassificationReimplementationTokens {
		if strings.Contains(folded, banned) {
			t.Errorf("%s %s first example uses %q, so the example a reader copies still re-implements "+
				"detection; requirement 1.6 forbids parsing User-Agent, rescanning content, or re-running the "+
				"classifier", sessionClassificationDocRel, sessionClassificationConsumerSection, banned)
		}
	}
}

// TestSessionClassificationDocs_incorrectExampleShowsTheAntiPattern is the mirror
// image: the example marked incorrect must actually contain one of the techniques
// the projection replaces, otherwise the section teaches nothing by contrast.
func TestSessionClassificationDocs_incorrectExampleShowsTheAntiPattern(t *testing.T) {
	t.Parallel()
	section := sessionClassificationGuideSection(t, sessionClassificationConsumerSection)

	fences := sessionClassificationGuideFences(section)
	if len(fences) < 2 {
		t.Fatalf("%s %s has %d code blocks, want a correct and an incorrect example",
			sessionClassificationDocRel, sessionClassificationConsumerSection, len(fences))
	}
	wrong := sessionClassificationFold(sessionClassificationGuideFenceLanguage(t, section,
		sessionClassificationConsumerSection, "go", 2))
	for _, banned := range sessionClassificationReimplementationTokens {
		if strings.Contains(wrong, banned) {
			return
		}
	}
	t.Errorf("%s %s second example is not actually incorrect: it contains none of %v, so the contrast teaches "+
		"nothing about what a consumer must not do",
		sessionClassificationDocRel, sessionClassificationConsumerSection, sessionClassificationReimplementationTokens)
}

// sessionClassificationPluginAuthoringRel is the plugin-authoring guide a future
// feature author is most likely to be reading instead of this guide.
const sessionClassificationPluginAuthoringRel = "docs/plugin-authoring.md"

// TestSessionClassificationDocs_pluginAuthoringPointsAtTheConsumerContract proves
// the consumer contract is discoverable from the guide plugin authors actually
// read. A correct contract nobody can find does not stop anyone re-implementing
// detection, which is the failure this feature exists to prevent.
func TestSessionClassificationDocs_pluginAuthoringPointsAtTheConsumerContract(t *testing.T) {
	t.Parallel()
	text := readRepoFile(t, repoRoot(t), sessionClassificationPluginAuthoringRel)

	section, ok := sessionClassificationSectionIn(text, "## Standard feature: session-classification")
	if !ok {
		t.Fatalf("%s has no session-classification section", sessionClassificationPluginAuthoringRel)
	}
	for _, marker := range []string{
		"session-classification.md",
		"IsCodingAgent()",
		"not authorization",
	} {
		if !strings.Contains(section, marker) {
			t.Errorf("%s session-classification section does not mention %q; a plugin author must reach the "+
				"consumer contract and its advisory-not-authorization rule from the guide they already read",
				sessionClassificationPluginAuthoringRel, marker)
		}
	}
}

// TestSessionClassificationDocs_explanationSurfaceIsFutureNotPrerequisite proves
// requirement 9.5's consumer is linked as a future surface and is nowhere treated
// as a dependency of this feature.
func TestSessionClassificationDocs_explanationSurfaceIsFutureNotPrerequisite(t *testing.T) {
	t.Parallel()
	section := sessionClassificationGuideSection(t, "## Future explanation surfaces")

	for _, marker := range []string{"#456", "future"} {
		if !strings.Contains(section, marker) {
			t.Errorf("%s does not mention %q; the operator-visible explanation surface is a documented future "+
				"consumer of the projection", sessionClassificationDocRel, marker)
		}
	}
	// The non-prerequisite claim has to sit next to the reference, not in a
	// footnote elsewhere in the document.
	sessionClassificationGuideLine(t, section, "## Future explanation surfaces",
		"non-prerequisite statement for the explanation surface", "#456", "not a prerequisite")
	for _, forbidden := range []string{"blocked on #456", "requires #456", "depends on #456"} {
		if strings.Contains(section, forbidden) {
			t.Errorf("%s states that this feature %q; #456 is a future consumer and never a prerequisite",
				sessionClassificationDocRel, forbidden)
		}
	}
	// The same projection rule applies to an explanation surface.
	sessionClassificationGuideLine(t, section, "## Future explanation surfaces",
		"projection rule for the explanation surface", "#456", "SessionView", "Classification")
}
