package secretguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	blconfig "github.com/betterleaks/betterleaks/v2/config"
	"github.com/betterleaks/betterleaks/v2/report"
	blscan "github.com/betterleaks/betterleaks/v2/scan"
	"github.com/betterleaks/betterleaks/v2/sources"
)

const adapterGitHubToken = "ghp_aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"

const (
	adapterAWSAccessID      = "AKIALALEMEL33243OLIA"
	adapterAWSSecretFixture = "aws_secret_access_key = \"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\""
)

func TestNewBetterLeaksScanner_AllowMarkersDoNotSuppressFindings(t *testing.T) {
	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: blscan.ConfidenceMedium,
		MaxDecodeDepth:    1,
		Workers:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detector == nil {
		t.Fatal("enabled policy must construct a scanner")
	}

	for _, marker := range []string{"betterleaks:allow", "gitleaks:allow"} {
		t.Run(marker, func(t *testing.T) {
			findings := scanBetterLeaksTestFragment(t, detector, "GITHUB_TOKEN="+adapterGitHubToken+" // "+marker)
			if len(findings) == 0 {
				t.Fatalf("marker %q suppressed a detectable credential", marker)
			}
		})
	}
}

func scanBetterLeaksTestFragment(t *testing.T, detector *betterLeaksScanner, content string) []report.Finding {
	t.Helper()
	var findings []report.Finding
	_, err := detector.scanner.Scan(context.Background(), betterLeaksLogicalFragmentSource{raw: []byte(content)}, func(finding report.Finding) error {
		findings = append(findings, finding)
		return nil
	})
	if err != nil {
		t.Fatalf("scan fragment: %v", err)
	}
	return findings
}

func defaultBetterLeaksConfig() (*blconfig.Config, error) {
	return blconfig.Default()
}

func betterLeaksRule(cfg *blconfig.Config, ruleID string) blconfig.Rule {
	rule, ok := cfg.Rule(ruleID)
	if !ok {
		panic("missing BetterLeaks rule " + ruleID)
	}
	return rule
}

func policyFinding(findings []report.Finding, ruleID string) (report.Finding, bool) {
	for _, finding := range findings {
		if finding.RuleID == ruleID {
			return finding, true
		}
	}
	return report.Finding{}, false
}

func componentFindingIDs(finding report.Finding) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, set := range finding.ComponentSets {
		for _, component := range set.Components {
			if _, ok := seen[component.RuleID]; ok {
				continue
			}
			seen[component.RuleID] = struct{}{}
			ids = append(ids, component.RuleID)
		}
	}
	sort.Strings(ids)
	return ids
}

func TestNewBetterLeaksScanner_RejectsInvalidConstructionPolicy(t *testing.T) {
	tests := []struct {
		name   string
		policy BetterLeaksPolicy
	}{
		{
			name: "zero workers",
			policy: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: blscan.ConfidenceMedium,
				MaxDecodeDepth:    1,
				Workers:           0,
			},
		},
		{
			name: "negative workers",
			policy: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: blscan.ConfidenceMedium,
				MaxDecodeDepth:    1,
				Workers:           -1,
			},
		},
		{
			name: "invalid confidence",
			policy: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: "unknown",
				MaxDecodeDepth:    1,
				Workers:           1,
			},
		},
		{
			name: "negative decode depth",
			policy: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: blscan.ConfidenceMedium,
				MaxDecodeDepth:    -1,
				Workers:           1,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if detector, err := newBetterLeaksScanner(test.policy); err == nil || detector != nil {
				t.Fatalf("newBetterLeaksScanner(%+v) = detector=%v, err=%v; want construction error", test.policy, detector, err)
			}
		})
	}
}

func TestNewBetterLeaksScanner_DisabledPolicyDoesNotConstructHandle(t *testing.T) {
	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if detector != nil {
		t.Fatal("disabled policy must not construct a scanner handle")
	}
}

func TestBetterLeaksScanner_ScanCanceledContext(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := detector.scanFragments(ctx, []LogicalFragment{{
		Location: "messages[0].parts[0]",
		Raw:      []byte("GITHUB_TOKEN=" + adapterGitHubToken),
	}})
	if err == nil {
		t.Fatal("canceled scan returned nil error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan error = %v; want context.Canceled", err)
	}
	if strings.Contains(err.Error(), adapterGitHubToken) {
		t.Fatalf("canceled scan error leaked secret: %v", err)
	}
}

func TestBetterLeaksScanner_ScanSourceErrorIsSanitizedAndIdentifiable(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	sourceErr := errors.New("source failed while reading " + adapterGitHubToken)
	result := betterLeaksScanResult{}

	err := detector.scanSource(context.Background(), adapterErrorSource{err: sourceErr}, "messages[0]", &result)
	if err == nil {
		t.Fatal("source failure returned nil error")
	}
	if !errors.Is(err, sourceErr) {
		t.Fatalf("source error = %v; errors.Is does not preserve source identity", err)
	}
	if strings.Contains(err.Error(), adapterGitHubToken) || strings.Contains(err.Error(), "source failed") {
		t.Fatalf("source error leaked upstream detail: %v", err)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("source failure produced %d findings", len(result.Findings))
	}
}

func TestBetterLeaksConstructionErrorIsSanitizedAndIdentifiable(t *testing.T) {
	t.Parallel()

	secret := "scanner-construction-secret-canary"
	sourceErr := errors.New("upstream configuration contains " + secret)
	err := newBetterLeaksConstructionError(sourceErr)
	if err == nil {
		t.Fatal("construction error sanitizer returned nil")
	}
	if !errors.Is(err, sourceErr) {
		t.Fatal("sanitized construction error must preserve private error identity")
	}
	if errors.Unwrap(err) != nil {
		t.Fatal("sanitized construction error must not expose an unwrap chain")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "upstream configuration") {
		t.Fatal("construction error exposed upstream detail")
	}
	if wrapped := fmt.Errorf("generation rejected: %w", err); strings.Contains(wrapped.Error(), secret) {
		t.Fatal("generic wrapping exposed upstream detail")
	}
}

func TestBetterLeaksScanner_FindingCapSpansSourceFragments(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	result := betterLeaksScanResult{}
	err := detector.scanSource(context.Background(), adapterRepeatedSource{
		count: 257,
		raw:   "GITHUB_TOKEN=" + adapterGitHubToken,
	}, "messages[0].parts[0]", &result)
	if err == nil {
		t.Fatal("finding cap was not enforced")
	}
	if !errors.Is(err, errBetterLeaksFindingCap) {
		t.Fatalf("finding cap error = %v; want cap classification", err)
	}
	if len(result.Findings) != DefaultBetterLeaksMaxFindings {
		t.Fatalf("projected findings = %d; want %d", len(result.Findings), DefaultBetterLeaksMaxFindings)
	}
	if strings.Contains(err.Error(), adapterGitHubToken) {
		t.Fatalf("finding cap error leaked secret: %v", err)
	}
}

func TestBetterLeaksScanner_ProjectsRequiredComponentsAndSafeMetadata(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	result, err := detector.scanFragments(context.Background(), []LogicalFragment{{
		Location: "messages[0].parts[0]",
		Raw:      []byte(awsMultipartFragment),
	}})
	if err != nil {
		t.Fatal(err)
	}
	finding, ok := findBetterLeaksProjectedFinding(result.Findings, "aws-access-token")
	if !ok {
		t.Fatalf("projected findings did not include aws-access-token (count=%d)", len(result.Findings))
	}
	if !containsBetterLeaksComponent(finding.Components, "aws-secret-access-key") {
		t.Fatalf("required multipart component was not projected (count=%d)", len(finding.Components))
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), adapterGitHubToken) || strings.Contains(string(encoded), "AKIALALEMEL33243OLIA") {
		t.Fatalf("projected result contains raw secret material: %s", encoded)
	}
}

func TestBetterLeaksScanner_PreservesWholeLogicalFragmentContext(t *testing.T) {
	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: blscan.ConfidenceMedium,
		MaxDecodeDepth:    1,
		Workers:           1,
		IsolateRules:      []string{"aws-access-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, padding := range []int{124940, 124950, 124960} {
		t.Run(fmt.Sprintf("padding_%d", padding), func(t *testing.T) {
			raw := strings.Repeat("x", padding) + "\n" + adapterAWSAccessID + "\n\n" + adapterAWSSecretFixture
			result, err := detector.scanFragments(context.Background(), []LogicalFragment{{
				Location: "messages[0].parts[0]",
				Raw:      []byte(raw),
			}})
			if err != nil {
				t.Fatal(err)
			}
			finding, ok := findBetterLeaksProjectedFinding(result.Findings, "aws-access-token")
			if !ok {
				t.Fatalf("whole logical fragment lost multipart context at padding %d (count=%d)", padding, len(result.Findings))
			}
			if !containsBetterLeaksComponent(finding.Components, "aws-secret-access-key") {
				t.Fatalf("whole logical fragment lost required component at padding %d (count=%d)", padding, len(finding.Components))
			}
		})
	}
}

func TestBetterLeaksScanner_PreservesLongLineCoordinates(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	raw := strings.Repeat("x", 1_500_000) + " GITHUB_TOKEN=" + adapterGitHubToken
	result, err := detector.scanFragments(context.Background(), []LogicalFragment{{
		Location: "messages[0].parts[0]",
		Raw:      []byte(raw),
	}})
	if err != nil {
		t.Fatal(err)
	}
	finding, ok := findBetterLeaksProjectedFinding(result.Findings, "github-pat")
	if !ok {
		t.Fatalf("long-line scan did not project github-pat (count=%d)", len(result.Findings))
	}
	wantStart := strings.Index(raw, adapterGitHubToken) + 1
	wantEnd := wantStart + len(adapterGitHubToken) - 1
	for _, occurrence := range finding.occurrences {
		if string(occurrence.value) != adapterGitHubToken {
			continue
		}
		if occurrence.span.StartColumn != wantStart || occurrence.span.EndColumn != wantEnd {
			t.Fatalf("long-line occurrence span = %+v; want columns %d..%d", occurrence.span, wantStart, wantEnd)
		}
		return
	}
	t.Fatal("long-line projection lost the github token occurrence")
}

func TestBetterLeaksLogicalFragmentSource_YieldsOneWholeFragment(t *testing.T) {
	raw := []byte(strings.Repeat("x", 125000))
	var fragments []sources.Fragment
	err := (betterLeaksLogicalFragmentSource{raw: raw}).Fragments(context.Background(), func(fragment sources.Fragment, fragmentErr error) error {
		if fragmentErr != nil {
			return fragmentErr
		}
		fragments = append(fragments, fragment)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 1 {
		t.Fatalf("logical fragment source yielded %d fragments; want 1", len(fragments))
	}
	if fragments[0].Raw != string(raw) {
		t.Fatal("logical fragment source changed admitted raw content")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (betterLeaksLogicalFragmentSource{raw: raw}).Fragments(ctx, func(sources.Fragment, error) error {
		t.Fatal("canceled logical fragment source invoked its callback")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled logical fragment source error = %v; want context.Canceled", err)
	}
}

func TestProjectBetterLeaksFinding_PreservesPrivateOccurrenceIdentity(t *testing.T) {
	location := "messages[0].parts[0]"
	first, err := projectBetterLeaksFinding(report.Finding{
		RuleID:     "same-rule",
		Confidence: blscan.ConfidenceMedium,
		Location:   report.Location{StartLine: 4, EndLine: 4, StartColumn: 2, EndColumn: 15},
		Match:      report.Match{Value: "literal-secret-a"},
		ComponentSets: []report.ComponentSet{{Components: []report.ComponentFinding{{
			RuleID:      "same-component",
			DecodeDepth: 1,
			Location:    report.Location{StartLine: 4, EndLine: 4, StartColumn: 20, EndColumn: 34},
			Match:       report.Match{Value: "decoded-secret-a"},
		}}}},
	}, location, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := projectBetterLeaksFinding(report.Finding{
		RuleID:     "same-rule",
		Confidence: blscan.ConfidenceMedium,
		Location:   report.Location{StartLine: 4, EndLine: 4, StartColumn: 2, EndColumn: 15},
		Match:      report.Match{Value: "literal-secret-b"},
	}, location, nil)
	if err != nil {
		t.Fatal(err)
	}

	if first.RuleID != second.RuleID || first.Location != second.Location {
		t.Fatalf("same-rule findings lost canonical identity: first=%s/%s second=%s/%s", first.RuleID, first.Location, second.RuleID, second.Location)
	}
	if len(first.occurrences) != 2 || len(second.occurrences) != 1 {
		t.Fatalf("private occurrence projection = first=%d second=%d; want component and repeated occurrences", len(first.occurrences), len(second.occurrences))
	}
	if string(first.occurrences[0].value) != "literal-secret-a" || first.occurrences[0].representation != betterLeaksOccurrenceLiteral {
		t.Fatalf("literal primary occurrence was not retained: representation=%d", first.occurrences[0].representation)
	}
	if first.occurrences[0].span != (betterLeaksSpan{StartLine: 4, EndLine: 4, StartColumn: 2, EndColumn: 15}) {
		t.Fatalf("primary occurrence span = %+v; want source span", first.occurrences[0].span)
	}
	if first.occurrences[1].role != betterLeaksOccurrenceComponent || first.occurrences[1].representation != betterLeaksOccurrenceDecoded {
		t.Fatalf("decoded component identity was not retained: role=%d representation=%d", first.occurrences[1].role, first.occurrences[1].representation)
	}
	if string(first.occurrences[0].value) == string(second.occurrences[0].value) {
		t.Fatal("distinct same-rule values were collapsed")
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "literal-secret-a") || strings.Contains(string(encoded), "decoded-secret-a") {
		t.Fatalf("private occurrence values were marshaled: %s", encoded)
	}
}

func TestProjectBetterLeaksFinding_PreservesDistinctLongValues(t *testing.T) {
	prefix := strings.Repeat("long-secret-", 400)
	firstValue := prefix + "suffix-a"
	secondValue := prefix + "suffix-b"
	first, err := projectBetterLeaksFinding(report.Finding{
		RuleID: "same-rule",
		Match:  report.Match{Value: firstValue},
	}, "messages[0].parts[0]", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := projectBetterLeaksFinding(report.Finding{
		RuleID: "same-rule",
		Match:  report.Match{Value: secondValue},
	}, "messages[0].parts[0]", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.occurrences) != 1 || len(second.occurrences) != 1 {
		t.Fatalf("long-value occurrence projection = first=%d second=%d; want one each", len(first.occurrences), len(second.occurrences))
	}
	if string(first.occurrences[0].value) != firstValue || string(second.occurrences[0].value) != secondValue {
		t.Fatal("long-value occurrence identity was truncated")
	}
	if string(first.occurrences[0].value) == string(second.occurrences[0].value) {
		t.Fatal("distinct long-value suffixes were collapsed")
	}
}

func TestProjectBetterLeaksFinding_RejectsUnrepresentableIdentity(t *testing.T) {
	tooLarge := strings.Repeat("s", maxBetterLeaksOccurrenceBytes+1)
	if _, err := projectBetterLeaksFinding(report.Finding{
		RuleID: "oversized-rule",
		Match:  report.Match{Value: tooLarge},
	}, "messages[0].parts[0]", nil); !errors.Is(err, errBetterLeaksProjection) {
		t.Fatalf("oversized occurrence error = %v; want bounded projection failure", err)
	}

	if _, err := projectBetterLeaksFinding(report.Finding{
		RuleID: "out-of-bounds-rule",
		Location: report.Location{
			StartLine: 1, EndLine: 1, StartColumn: 33, EndColumn: 33,
		},
		Match: report.Match{Value: "secret"},
	}, "messages[0].parts[0]", []byte(strings.Repeat("x", 32))); !errors.Is(err, errBetterLeaksProjection) {
		t.Fatalf("out-of-bounds location error = %v; want bounded projection failure", err)
	}
}

func TestBetterLeaksScanner_PreservesDeterministicFragmentLocations(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	result, err := detector.scanFragments(context.Background(), []LogicalFragment{
		{Location: "messages[1].parts[0]", Raw: []byte("GITHUB_TOKEN=" + adapterGitHubToken)},
		{Location: "messages[0].parts[0]", Raw: []byte("GITHUB_TOKEN=" + adapterGitHubToken)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("projected findings = %d; want 2", len(result.Findings))
	}
	if result.Findings[0].Location != "messages[0].parts[0]" || result.Findings[1].Location != "messages[1].parts[0]" {
		t.Fatalf("projected locations are not deterministic: %q, %q", result.Findings[0].Location, result.Findings[1].Location)
	}
}

func TestBetterLeaksScanner_IsSafeForConcurrentReuse(t *testing.T) {
	detector := newTestBetterLeaksScanner(t)
	const requests = 16
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := detector.scanFragments(context.Background(), []LogicalFragment{{
				Location: fmt.Sprintf("messages[%d].parts[0]", i),
				Raw:      []byte("GITHUB_TOKEN=" + adapterGitHubToken),
			}})
			if err == nil && len(result.Findings) == 0 {
				err = errors.New("concurrent scan produced no finding")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func newTestBetterLeaksScanner(t *testing.T) *betterLeaksScanner {
	t.Helper()
	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: blscan.ConfidenceMedium,
		MaxDecodeDepth:    1,
		Workers:           4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detector == nil {
		t.Fatal("test scanner is nil")
	}
	return detector
}

func findBetterLeaksProjectedFinding(findings []betterLeaksFinding, ruleID string) (betterLeaksFinding, bool) {
	for _, finding := range findings {
		if finding.RuleID == ruleID {
			return finding, true
		}
	}
	return betterLeaksFinding{}, false
}

func containsBetterLeaksComponent(components []betterLeaksComponent, ruleID string) bool {
	for _, component := range components {
		if component.RuleID == ruleID {
			return true
		}
	}
	return false
}

type adapterErrorSource struct {
	err error
}

func (s adapterErrorSource) Fragments(ctx context.Context, yield sources.FragmentsFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return yield(sources.Fragment{}, s.err)
}

type adapterRepeatedSource struct {
	count int
	raw   string
}

func (s adapterRepeatedSource) Fragments(ctx context.Context, yield sources.FragmentsFunc) error {
	for i := 0; i < s.count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := yield(sources.Fragment{Raw: s.raw}, nil); err != nil {
			return err
		}
	}
	return nil
}
