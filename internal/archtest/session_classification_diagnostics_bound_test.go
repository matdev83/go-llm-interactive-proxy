package archtest

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	featureclassification "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

// Task 11.2 (requirements 9.2, 9.4, 7.5, 11.3-11.5): a guide that documents the
// bounded surface while itself publishing raw identities, absolute paths, prompt
// text, or session identifiers as example label values teaches the opposite of
// what requirement 9.4 requires. These checks therefore scan the whole guide,
// not only the observability section: the prohibition is about what the document
// models, not about where it happens to be written.

// sessionClassificationGuideFenceLanguages are the fence info strings the guide
// may use. A bare fence is refused because it is indistinguishable from a log
// line pasted in without context.
var sessionClassificationGuideFenceLanguages = map[string]bool{
	"go":      true,
	"yaml":    true,
	"console": true,
	"promql":  true,
	"text":    true,
}

// sessionClassificationOriginFamilies binds each evidence-code origin to the
// classification source its codes are published under. Both sides come from
// shipped constants, so an evidence code cannot be documented under the wrong
// origin.
var sessionClassificationOriginFamilies = map[string]string{
	"client_family.": string(session.SourceLocalIdentity),
	"tooling.":       string(session.SourceLocalTooling),
	"remote.":        string(session.SourceRemote),
}

// sessionClassificationHighCardinalityForbid rejects a literal assigned to a
// label-shaped key on one line. Every match in this guide must be a placeholder
// such as "<absolute workspace path>"; a real value is exactly the example
// requirement 9.4 forbids publishing.
//
// The spacing classes are [ \t], never \s: a newline-crossing match would fire on
// ordinary prose such as "confidential path:\n\n- the workspace root", which is
// not an example at all.
var sessionClassificationHighCardinalityForbid = regexp.MustCompile(
	"(?i)\\b(user[_-]?agent|path|filename|prompt|a_leg|aleg_id|session_id|sessionid|" +
		"authoritativesessionid|revision|evidencecode)\\b[\"`']?[ \\t]*[:=][ \\t]*[\"'`]?([^\\s\"'`|)]+)")

// sessionClassificationAbsolutePaths rejects a concrete filesystem location in
// any example position.
var sessionClassificationAbsolutePaths = regexp.MustCompile(
	"(?i)(^|[\\s\"'`(=])(/home/|/users/|/root/|/var/|/tmp/|/etc/|/private/|[a-z]:\\\\)")

// sessionClassificationVersionedProducts rejects a copied User-Agent, which is
// recognisable by its product token followed by a version.
var sessionClassificationVersionedProducts = regexp.MustCompile(
	"(?i)\\b(codex_cli_rs|codex-cli|codex|opencode|open-code|cline|roo-code|roocode|roo|droid|" +
		"hermes|copilot|cursor|windsurf|aider|continue|go-http-client|python-requests|node-fetch|" +
		"okhttp|reqwest|axios|java|got|undici)[/ _-]v?[0-9]")

// TestSessionClassificationDocs_publishesNoHighCardinalityExample is the
// non-encouragement half of the observability contract: the guide must not model
// a raw identity, an absolute path, a prompt, or a session/A-leg identifier as
// something to put in a metric label or a log line.
func TestSessionClassificationDocs_publishesNoHighCardinalityExample(t *testing.T) {
	t.Parallel()
	text := sessionClassificationGuideSlice(t)

	for _, match := range sessionClassificationHighCardinalityForbid.FindAllStringSubmatch(text, -1) {
		value := match[2]
		if strings.HasPrefix(value, "<") || strings.HasPrefix(value, "{") {
			continue
		}
		t.Errorf("%s publishes the literal %q as a value for %q; requirement 9.4 forbids session IDs, A-leg "+
			"IDs, raw User-Agent, filenames, paths, and prompts as labels. Use an explicit placeholder instead",
			sessionClassificationDocRel, value, match[1])
	}
	if match := sessionClassificationAbsolutePaths.FindString(text); match != "" {
		t.Errorf("%s contains the concrete filesystem location %q; a published guide must model a path as a "+
			"shape, never as a real value", sessionClassificationDocRel, match)
	}
	if match := sessionClassificationVersionedProducts.FindString(text); match != "" {
		t.Errorf("%s contains %q, which is shaped like a copied client User-Agent product token; evidence "+
			"codes are the only identity this feature publishes", sessionClassificationDocRel, match)
	}
}

// TestSessionClassificationDocs_everyCodeFenceDeclaresALanguage keeps every
// example self-describing, so a reader cannot mistake a snippet for a value to
// paste into a label.
func TestSessionClassificationDocs_everyCodeFenceDeclaresALanguage(t *testing.T) {
	t.Parallel()
	for _, fence := range sessionClassificationGuideFences(sessionClassificationGuideSlice(t)) {
		if !sessionClassificationGuideFenceLanguages[fence.Language] {
			t.Errorf("%s line %d opens a code fence with info string %q; declare one of %s so a reader can "+
				"tell a configuration snippet from an example observation",
				sessionClassificationDocRel, fence.Line, fence.Language,
				strings.Join(sortedLanguageList(), ", "))
		}
	}
}

func sortedLanguageList() []string {
	languages := make([]string, 0, len(sessionClassificationGuideFenceLanguages))
	for language := range sessionClassificationGuideFenceLanguages {
		languages = append(languages, "`"+language+"`")
	}
	slices.Sort(languages)
	return languages
}

// TestSessionClassificationDocs_evidenceCodesAreAttributedToTheirOrigin proves
// every shipped decisive code is documented with the classification source it is
// actually published under, so a reader learns which rule produced a positive
// rather than only that one did.
func TestSessionClassificationDocs_evidenceCodesAreAttributedToTheirOrigin(t *testing.T) {
	t.Parallel()
	section := sessionClassificationGuideSection(t, "## Evidence-code semantics")

	for _, code := range featureclassification.BoundedEvidenceCodes() {
		origin, known := sessionClassificationCodeOrigin(code)
		if !known {
			t.Fatalf("evidence code %q belongs to no documented origin family; add its shipped origin to "+
				"sessionClassificationOriginFamilies", code)
		}
		if !strings.Contains(section, "`"+code+"`") {
			t.Errorf("%s does not document shipped evidence code %q", sessionClassificationDocRel, code)
			continue
		}
		sessionClassificationGuideLine(t, section, "## Evidence-code semantics",
			"attribution of evidence code "+code, "`"+code+"`", "`"+origin+"`")
	}
}

// sessionClassificationCodeOrigin resolves the classification source an evidence
// code is published under from its shipped code prefix.
func sessionClassificationCodeOrigin(code string) (string, bool) {
	best := ""
	origin := ""
	for prefix, candidate := range sessionClassificationOriginFamilies {
		if strings.HasPrefix(code, prefix) && len(prefix) > len(best) {
			best, origin = prefix, candidate
		}
	}
	return origin, best != ""
}

// TestSessionClassificationDocs_evidenceCodesAreBoundedMetadataNotContent proves
// the guide states what an evidence code is - a bounded identifier naming the
// rule, never the content that triggered it - which is what keeps the surface
// diagnosable without becoming a content channel.
func TestSessionClassificationDocs_evidenceCodesAreBoundedMetadataNotContent(t *testing.T) {
	t.Parallel()
	section := sessionClassificationGuideSection(t, "## Evidence-code semantics")

	for _, marker := range []string{
		// A code names the rule, not the input.
		"names the rule",
		// Nothing a code matches is reproduced by it.
		"never contains",
		// A code is not an authorization signal.
		"not authorization",
	} {
		if !strings.Contains(section, marker) {
			t.Errorf("%s does not state %q about evidence codes; a code is bounded diagnostic metadata, not "+
				"content and not an authority", sessionClassificationDocRel, marker)
		}
	}
	// The shared byte bound must be attached to its shipped value, exactly as
	// task 11.1 pinned the other bounds.
	if !regexp.MustCompile("`MaxEvidenceCodeBytes`[^|\\n]*\\|[^|\\n]*" +
		regexp.QuoteMeta(strconv.Itoa(session.MaxEvidenceCodeBytes))).MatchString(section) {
		t.Errorf("%s does not attach the shipped MaxEvidenceCodeBytes (%d) to a table row; an evidence code is "+
			"bounded by the SDK contract, not by prose",
			sessionClassificationDocRel, session.MaxEvidenceCodeBytes)
	}
}

// TestSessionClassificationDocs_disabledPostureExplainsAbsentObservations proves
// requirements 9.6 and 11.7 are documented: with the feature disabled every
// classification-specific series is absent, and only static inventory state may
// remain visible.
func TestSessionClassificationDocs_disabledPostureExplainsAbsentObservations(t *testing.T) {
	t.Parallel()
	section := sessionClassificationGuideSection(t, "## Disabled posture")

	families := parseSessionClassificationMetrics(t)
	static := 0
	for _, family := range families {
		line := sessionClassificationGuideLine(t, section, "## Disabled posture",
			"statement about metric "+family.Name, family.Name)
		switch len(family.Labels) {
		case 0:
			// A label-free family is static state by construction, and requirement
			// 9.6 keeps it visible while everything else stays absent.
			static++
			if !strings.Contains(line, "static") {
				t.Errorf("%s does not explain that %q is the static family that may remain visible while the "+
					"feature is disabled", sessionClassificationDocRel, family.Name)
			}
		default:
			if !strings.Contains(line, "absent") {
				t.Errorf("%s does not state that %q exports no series while the feature is disabled; "+
					"requirement 9.6 keeps classification-specific hot-path observations absent",
					sessionClassificationDocRel, family.Name)
			}
		}
	}
	if static == 0 {
		t.Errorf("%s does not identify any static family that survives the disabled posture", sessionClassificationDocRel)
	}
	for _, marker := range []string{
		// 11.7: the generic proxy never depends on classification infrastructure.
		"continue to route, stream, recover, account, and encode",
	} {
		if !strings.Contains(section, marker) {
			t.Errorf("%s does not state %q; requirement 11.7 requires the proxy to keep working without "+
				"classification infrastructure", sessionClassificationDocRel, marker)
		}
	}
}
