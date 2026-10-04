package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	featureclassification "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"gopkg.in/yaml.v3"
)

// Task 11.1 (requirements 1.1-1.8, 6.1-6.11, 7.1-7.7, 8.1-8.8, 11.7): the
// operator guide for session classification is only useful if it is present AND
// true. This contract proves both halves:
//
//   - presence: every required section heading, config key, example reference
//     and the two explicit safety statements must be in docs/session-classification.md;
//   - truth: the numeric bounds and closed vocabularies the document states must
//     be read out of the shipped source constants rather than restated here, so
//     a changed default or renamed evidence code fails this test instead of
//     leaving stale prose behind for operators to trust.
//
// The file read is a tree walk on purpose: docs/ is not part of any Go package,
// so a string-contract test is the only machine check available.

const sessionClassificationDocRel = "docs/session-classification.md"

// sessionClassificationDocSections are the operator-guide sections task 11.1
// requires. Each entry is the exact markdown heading line.
var sessionClassificationDocSections = []string{
	"## Scope: advisory metadata, not authorization",
	"## Classification semantics",
	"## Authoritative session keying",
	"## Local evidence rules",
	"## Configuration",
	"## Remote (Jev) modes",
	"## Fail-open behavior",
	"## Durable and non-durable posture",
	"## Reload behavior",
	"## Large-body (wire) compatibility",
	"## Privacy and data minimization",
}

// sessionClassificationDocConfigKeys are the canonical feature-owned
// configuration keys task 11.1 requires the guide to document, plus the
// outer registration entry that owns enablement.
var sessionClassificationDocConfigKeys = []string{
	"plugins.features",
	"id: session-classification",
	"enabled: true",
	"config.mode",
	"config.heuristic.ignored_user_agent_prefixes",
	"config.remote.provider",
	"config.remote.api_key_env",
	"config.remote.timeout",
	"config.remote.max_attempts_per_session",
	"config.remote.lease_ttl",
	"config.remote.retry_backoff",
	"config.remote.positive_threshold",
}

// sessionClassificationDocRequiredMarkers are non-negotiable prose facts: the
// monotonic contract, the authoritative keying rule, the exclusion scope, the
// opt-in posture, and the two statements the task requires verbatim in
// substance (not authorization; existing features are not gated).
var sessionClassificationDocRequiredMarkers = []string{
	"coding_agent",
	"unknown",
	"Absence of evidence is never a negative classification",
	"never downgraded",
	"ClientSessionHint is never a state key",
	"AuthoritativeSessionID",
	"ALegID",
	"exclusion is checked before Rule A",
	"heuristic",
	"jev",
	"hybrid",
	"zero egress",
	"explicitly configured",
	"environment-variable name",
	"fails open",
	"not authorization",
	"explicitly opts in",
}

// sessionClassificationDocForbidden needles the guide must never contain. These
// keep a credential or a configurable endpoint out of published prose and out of
// the diagnostics a reader is told to expect.
var sessionClassificationDocForbidden = []string{
	"api_key:",
	"Authorization: Bearer sk-",
	"Bearer ts-",
	"endpoint is configurable",
}

func readSessionClassificationDoc(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, repoRoot(t), sessionClassificationDocRel)
}

// operatorDuration renders a shipped duration bound the way an operator
// configuration spells it, so the guide can say "2m" rather than Go's "2m0s"
// while still being checked against the source constant rather than restated.
func operatorDuration(d time.Duration) string {
	switch text := d.String(); {
	case strings.HasSuffix(text, "h0m0s") && isAllDigits(strings.TrimSuffix(text, "h0m0s")):
		return strings.TrimSuffix(text, "0m0s") + "h"
	case strings.HasSuffix(text, "m0s") && isAllDigits(strings.TrimSuffix(text, "m0s")):
		return strings.TrimSuffix(text, "0s")
	default:
		return text
	}
}

// isAllDigits reports whether value is a non-empty run of ASCII digits, so a
// shortened duration form is only accepted for an exact whole-unit value.
func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func TestSessionClassificationDocs_documentEveryRequiredSection(t *testing.T) {
	t.Parallel()
	text := readSessionClassificationDoc(t)
	for _, heading := range sessionClassificationDocSections {
		if !strings.Contains(text, heading) {
			t.Fatalf("%s missing required section heading %q", sessionClassificationDocRel, heading)
		}
	}
	for _, key := range sessionClassificationDocConfigKeys {
		if !strings.Contains(text, key) {
			t.Fatalf("%s missing required configuration key %q", sessionClassificationDocRel, key)
		}
	}
	for _, marker := range sessionClassificationDocRequiredMarkers {
		if !strings.Contains(text, marker) {
			t.Fatalf("%s missing required operator statement %q", sessionClassificationDocRel, marker)
		}
	}
	for _, forbidden := range sessionClassificationDocForbidden {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s must not contain %q", sessionClassificationDocRel, forbidden)
		}
	}
}

// TestSessionClassificationDocs_boundsMatchSourceConstants is the truthfulness
// half of the contract. Every number below is read from the shipped feature
// policy, formatted here, and required in the prose. A changed bound therefore
// breaks this test, which forces the guide to be corrected rather than left
// quietly stale.
func TestSessionClassificationDocs_boundsMatchSourceConstants(t *testing.T) {
	t.Parallel()
	text := readSessionClassificationDoc(t)

	bounds := []struct {
		constant string
		value    string
	}{
		{"MaxIgnoredUserAgentPrefixes", fmt.Sprintf("%d", featureclassification.MaxIgnoredUserAgentPrefixes)},
		{"MaxIgnoredUserAgentPrefixBytes", fmt.Sprintf("%d", featureclassification.MaxIgnoredUserAgentPrefixBytes)},
		{"MaxWorkspaceMarkers", fmt.Sprintf("%d", featureclassification.MaxWorkspaceMarkers)},
		{"MaxWorkspaceMarkerBytes", fmt.Sprintf("%d", featureclassification.MaxWorkspaceMarkerBytes)},
		{"MaxRemoteAttemptsPerSession", fmt.Sprintf("%d", featureclassification.MaxRemoteAttemptsPerSession)},
		{"MinRemoteTimeout", operatorDuration(featureclassification.MinRemoteTimeout)},
		{"MaxRemoteTimeout", operatorDuration(featureclassification.MaxRemoteTimeout)},
		{"MaxRemoteLeaseTTL", operatorDuration(featureclassification.MaxRemoteLeaseTTL)},
		{"MaxRemoteRetryBackoff", operatorDuration(featureclassification.MaxRemoteRetryBackoff)},
		{"RemoteLeaseSafetyMargin", operatorDuration(featureclassification.RemoteLeaseSafetyMargin)},
		{"MaxAuthorityIDBytes", fmt.Sprintf("%d", featureclassification.MaxAuthorityIDBytes)},
		{"MaxRemoteLeaseIDBytes", fmt.Sprintf("%d", featureclassification.MaxRemoteLeaseIDBytes)},
		{"MaxEvidenceCodeBytes", fmt.Sprintf("%d", session.MaxEvidenceCodeBytes)},
	}
	// The value must be ATTACHED to its constant, not merely present somewhere in
	// the document. Two independent Contains calls would let an operator be told
	// `MaxWorkspaceMarkers` is 128 while the real 32 appears elsewhere, which is
	// exactly the stale-documentation failure this test exists to prevent.
	for _, bound := range bounds {
		if !strings.Contains(text, bound.constant) {
			t.Fatalf("%s does not cite source constant %s", sessionClassificationDocRel, bound.constant)
		}
		attached := regexp.MustCompile("`" + regexp.QuoteMeta(bound.constant) + "`[^|\\n]*\\|[^|\\n]*" + regexp.QuoteMeta(bound.value))
		if !attached.MatchString(text) {
			t.Fatalf("%s cites %s but never attaches the shipped value %s to it; an operator reading the "+
				"guide must not be able to find a stale or invented value (bound %s)",
				sessionClassificationDocRel, bound.constant, bound.value, bound.value)
		}
	}
}

// TestSessionClassificationDocs_vocabulariesMatchSource proves the guide names
// exactly the shipped closed vocabularies: the three modes, every decisive
// evidence code, the classification kind/source/confidence vocabulary, and the
// extension stage and plane identifiers.
func TestSessionClassificationDocs_vocabulariesMatchSource(t *testing.T) {
	t.Parallel()
	text := readSessionClassificationDoc(t)

	for _, mode := range featureclassification.ObservationModes() {
		if !strings.Contains(text, mode) {
			t.Fatalf("%s does not document shipped mode %q", sessionClassificationDocRel, mode)
		}
	}
	for _, code := range featureclassification.BoundedEvidenceCodes() {
		if !strings.Contains(text, code) {
			t.Fatalf("%s does not document shipped evidence code %q", sessionClassificationDocRel, code)
		}
	}
	// The shipped unknown kind is the empty string, so requiring it as a literal
	// needle would be vacuous. Tie the guide's "unknown is the zero value" claim
	// to the constant instead, and require the positive kind literally.
	if session.KindUnknown != "" {
		t.Fatalf("shipped unknown kind is expected to be the zero value, got %q", session.KindUnknown)
	}
	if !strings.Contains(text, "zero value") {
		t.Fatalf("%s does not state that unknown is the all-zero classification", sessionClassificationDocRel)
	}
	if !strings.Contains(text, string(session.KindCodingAgent)) {
		t.Fatalf("%s does not document shipped classification kind %q", sessionClassificationDocRel, session.KindCodingAgent)
	}
	for _, source := range []session.ClassificationSource{
		session.SourceLocalIdentity,
		session.SourceLocalTooling,
		session.SourceRemote,
	} {
		if !strings.Contains(text, string(source)) {
			t.Fatalf("%s does not document shipped classification source %q", sessionClassificationDocRel, source)
		}
	}
	if !strings.Contains(text, string(session.ConfidenceHigh)) {
		t.Fatalf("%s does not document shipped confidence band %q", sessionClassificationDocRel, session.ConfidenceHigh)
	}
	if !strings.Contains(text, lipfeature.StageIDSessionClassification) {
		t.Fatalf("%s does not name the shipped stage id %q", sessionClassificationDocRel, lipfeature.StageIDSessionClassification)
	}
	if !strings.Contains(text, lipfeature.PlaneSessionClassifier.ID) {
		t.Fatalf("%s does not name the shipped plane id %q", sessionClassificationDocRel, lipfeature.PlaneSessionClassifier.ID)
	}
	if !strings.Contains(text, lipfeature.RequestBodyMetadataOnly.String()) {
		t.Fatalf("%s does not name the shipped request-access class %q", sessionClassificationDocRel, lipfeature.RequestBodyMetadataOnly)
	}
}

// sessionClassificationExamples are the canonical operator examples task 11.1
// requires: one heuristic, one explicit remote mode.
//
// Both files are picked up automatically by the configuration example suite in
// internal/infra/runtimebundle/example_configs_test.go, which loads and inspects
// every YAML in config/examples. That suite installs the plugin registry but does
// not invoke feature factories, so it proves the file loads but does not prove
// the feature config is semantically valid. TestSessionClassificationDocs_configExamplesDecodeThroughShippedValidation
// closes that gap by decoding the same YAML node the standard feature factory
// receives.
var sessionClassificationExamples = []string{
	"config/examples/session-classification-heuristic.yaml",
	"config/examples/session-classification-remote-jev.yaml",
}

// TestSessionClassificationDocs_configExamplesAreCanonicalAndCredentialFree
// keeps the two published examples aligned with the shipped schema. It asserts
// the feature entry is enabled under plugins.features, that the remote example
// names every required remote field, and that neither example embeds a
// credential value or configures an endpoint.
func TestSessionClassificationDocs_configExamplesAreCanonicalAndCredentialFree(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc := readSessionClassificationDoc(t)

	for _, rel := range sessionClassificationExamples {
		if !strings.Contains(doc, rel) {
			t.Fatalf("%s must reference the operator example %q", sessionClassificationDocRel, rel)
		}
		text := readRepoFile(t, root, rel)
		for _, needle := range []string{
			"plugins:",
			"  features:",
			"- id: " + featureclassification.ID,
			"enabled: true",
			"      config:",
		} {
			if !strings.Contains(text, needle) {
				t.Fatalf("%s missing canonical example fragment %q", rel, needle)
			}
		}
		for _, forbidden := range []string{"api_key:", "endpoint:", "Authorization:"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s must not contain %q", rel, forbidden)
			}
		}
	}

	if heuristic := readRepoFile(t, root, sessionClassificationExamples[0]); !strings.Contains(heuristic, "mode: heuristic") {
		t.Fatalf("%s must pin the default heuristic mode", sessionClassificationExamples[0])
	}

	remote := readRepoFile(t, root, sessionClassificationExamples[1])
	for _, needle := range []string{
		"provider: jev",
		"api_key_env:",
		"timeout:",
		"max_attempts_per_session:",
		"lease_ttl:",
		"retry_backoff:",
		"positive_threshold:",
	} {
		if !strings.Contains(remote, needle) {
			t.Fatalf("%s missing required remote field %q", sessionClassificationExamples[1], needle)
		}
	}
	// The example must declare exactly one explicit remote-capable mode. jev and
	// hybrid are the only two; a bare heuristic here would defeat the example.
	if !strings.Contains(remote, "mode: hybrid") && !strings.Contains(remote, "mode: jev") {
		t.Fatalf("%s must declare an explicit remote mode (jev or hybrid)", sessionClassificationExamples[1])
	}
}

// TestSessionClassificationDocs_remoteExampleRespectsShippedBounds restates the
// shipped remote validation as a check on the published example, so the guide
// cannot teach a combination the feature would refuse.
func TestSessionClassificationDocs_remoteExampleRespectsShippedBounds(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	remote := readRepoFile(t, root, sessionClassificationExamples[1])

	for _, needle := range []string{
		"timeout: 750ms",
		"max_attempts_per_session: 1",
		"lease_ttl: 2s",
		"retry_backoff: 0s",
		"positive_threshold: 0.90",
	} {
		if !strings.Contains(remote, needle) {
			t.Fatalf("%s missing canonical remote value %q", sessionClassificationExamples[1], needle)
		}
	}

	// lease_ttl must exceed timeout plus the safety margin, exactly as
	// RemoteConfig.validate enforces.
	timeout := 750 * time.Millisecond
	leaseTTL := 2 * time.Second
	if leaseTTL <= timeout+featureclassification.RemoteLeaseSafetyMargin {
		t.Fatalf("documented lease_ttl %s must exceed timeout %s plus the safety margin %s",
			leaseTTL, timeout, featureclassification.RemoteLeaseSafetyMargin)
	}
	if timeout < featureclassification.MinRemoteTimeout || timeout > featureclassification.MaxRemoteTimeout {
		t.Fatalf("documented timeout %s is outside the shipped bounds", timeout)
	}
	if leaseTTL > featureclassification.MaxRemoteLeaseTTL {
		t.Fatalf("documented lease_ttl %s exceeds the shipped cap", leaseTTL)
	}
}

// TestSessionClassificationDocs_configExamplesDecodeThroughShippedValidation is
// the machine check that makes the published examples real rather than
// illustrative. It extracts each example's `session-classification` feature
// config and runs it through the same DecodeConfig the standard feature factory
// uses, so an unknown key, a missing remote field, or an out-of-bounds value in
// a published example fails here. It then asserts the decoded posture equals what
// the guide claims, which means the documented mode, provider, credential
// reference, threshold, and lease relationship are all source-derived.
func TestSessionClassificationDocs_configExamplesDecodeThroughShippedValidation(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	heuristic, err := decodeExampleFeatureConfig(t, root, sessionClassificationExamples[0])
	if err != nil {
		t.Fatalf("decode %s: %v", sessionClassificationExamples[0], err)
	}
	if heuristic.Mode != featureclassification.ModeHeuristic {
		t.Fatalf("%s decodes mode %q, want %q", sessionClassificationExamples[0], heuristic.Mode, featureclassification.ModeHeuristic)
	}
	if heuristic.Remote != nil {
		t.Fatalf("%s must not configure a remote block in heuristic mode", sessionClassificationExamples[0])
	}

	remote, err := decodeExampleFeatureConfig(t, root, sessionClassificationExamples[1])
	if err != nil {
		t.Fatalf("decode %s: %v", sessionClassificationExamples[1], err)
	}
	if remote.Mode != featureclassification.ModeHybrid {
		t.Fatalf("%s decodes mode %q, want %q", sessionClassificationExamples[1], remote.Mode, featureclassification.ModeHybrid)
	}
	if remote.Remote == nil {
		t.Fatalf("%s must configure a remote block", sessionClassificationExamples[1])
	}
	if remote.Remote.Provider != "jev" {
		t.Fatalf("%s decodes provider %q, want the only supported provider jev", sessionClassificationExamples[1], remote.Remote.Provider)
	}
	// The published value must be a credential REFERENCE. The shipped validation
	// already refuses anything that is not an environment-variable name, so a
	// decode success is itself the proof that no credential value is published.
	if remote.Remote.APIKeyEnv == "" {
		t.Fatalf("%s must reference a credential by environment-variable name", sessionClassificationExamples[1])
	}
	if remote.Remote.Timeout <= 0 || remote.Remote.LeaseTTL <= remote.Remote.Timeout+featureclassification.RemoteLeaseSafetyMargin {
		t.Fatalf("%s decodes an unusable lease/timeout pair: lease_ttl=%s timeout=%s",
			sessionClassificationExamples[1], remote.Remote.LeaseTTL, remote.Remote.Timeout)
	}
	if remote.Remote.PositiveThreshold <= 0 || remote.Remote.PositiveThreshold > 1 {
		t.Fatalf("%s decodes positive_threshold %v outside (0, 1]", sessionClassificationExamples[1], remote.Remote.PositiveThreshold)
	}
	if remote.Remote.MaxAttemptsPerSession < 1 || remote.Remote.MaxAttemptsPerSession > featureclassification.MaxRemoteAttemptsPerSession {
		t.Fatalf("%s decodes max_attempts_per_session %d outside 1..%d",
			sessionClassificationExamples[1], remote.Remote.MaxAttemptsPerSession, featureclassification.MaxRemoteAttemptsPerSession)
	}
}

// decodeExampleFeatureConfig reads one published example and returns the
// decoded policy for its session-classification feature entry, using the exact
// YAML node the standard feature factory receives.
func decodeExampleFeatureConfig(t *testing.T, root, rel string) (featureclassification.Config, error) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return featureclassification.Config{}, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return featureclassification.Config{}, err
	}
	mapping := documentMapping(&document)
	plugins, ok := mappingChild(mapping, "plugins")
	if !ok {
		return featureclassification.Config{}, fmt.Errorf("no plugins mapping")
	}
	features, ok := mappingChild(plugins, "features")
	if !ok || features.Kind != yaml.SequenceNode {
		return featureclassification.Config{}, fmt.Errorf("no plugins.features sequence")
	}
	for _, entry := range features.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		if id, ok := mappingChild(entry, "id"); !ok || id.Value != featureclassification.ID {
			continue
		}
		cfgNode, ok := mappingChild(entry, "config")
		if !ok {
			return featureclassification.Config{}, fmt.Errorf("feature entry has no config mapping")
		}
		if enabled, ok := mappingChild(entry, "enabled"); !ok || enabled.Value != "true" {
			return featureclassification.Config{}, fmt.Errorf("feature entry is not enabled")
		}
		return featureclassification.DecodeConfig(*cfgNode)
	}
	return featureclassification.Config{}, fmt.Errorf("no %q feature entry", featureclassification.ID)
}

func documentMapping(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		return node.Content[0]
	}
	return node
}

func mappingChild(node *yaml.Node, key string) (*yaml.Node, bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1], true
		}
	}
	return nil, false
}

// TestSessionClassificationDocs_areTrackedRepositoryFile keeps the guide inside
// the declared boundary: it is a real repository file, not an untracked draft.
