package secretguard

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	blconfig "github.com/betterleaks/betterleaks/v2/config"
	blregexp "github.com/betterleaks/betterleaks/v2/regexp"
	"github.com/betterleaks/betterleaks/v2/report"
	blscan "github.com/betterleaks/betterleaks/v2/scan"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

func TestMultipartProjectionRepair_LocalCoverageBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		components  int
		unique      bool
		truncated   bool
		wantBlocked bool
	}{
		{name: "upstream truncated", truncated: true, wantBlocked: true},
		{name: "components at cap", components: maxBetterLeaksProjectedComponents, unique: true},
		{name: "components exceeding cap", components: maxBetterLeaksProjectedComponents + 1, unique: true, wantBlocked: true},
		{name: "occurrences at cap", components: maxBetterLeaksOccurrences - 1},
		{name: "occurrences exceeding cap", components: maxBetterLeaksOccurrences, wantBlocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finding := report.Finding{RuleID: "github-pat", Match: report.Match{Value: "x"}, Location: report.Location{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 1}, ComponentSetsTruncated: tc.truncated}
			for i := 0; i < tc.components; i++ {
				id := "component"
				if tc.unique {
					id = fmt.Sprintf("component-%d", i)
				}
				finding.ComponentSets = append(finding.ComponentSets, report.ComponentSet{Components: []report.ComponentFinding{{RuleID: id, Match: finding.Match, Location: finding.Location}}})
			}
			projected, err := projectBetterLeaksFindingWithFieldID(finding, "messages[0].parts[0]", "fragment[0]", []byte("x"))
			if err != nil {
				t.Fatal(err)
			}
			defer releaseBetterLeaksOccurrenceBytes([]betterLeaksFinding{projected})
			if len(projected.Components) > maxBetterLeaksProjectedComponents || len(projected.occurrences) > maxBetterLeaksOccurrences {
				t.Fatal("projection exceeded local bounds")
			}
			if got, want := len(projected.occurrences), min(tc.components+1, min(maxBetterLeaksOccurrences, maxBetterLeaksProjectedComponents+1)); tc.unique && got != want {
				t.Fatalf("unique component occurrences=%d; want %d", got, want)
			}
			if !tc.unique && len(projected.occurrences) != min(tc.components+1, maxBetterLeaksOccurrences) {
				t.Fatal("repeated component projection dropped occurrences before its cap")
			}
			err = validateBetterLeaksRedactionEligibility([]LogicalFragment{{Location: projected.Location, privateID: "fragment[0]", Raw: []byte("x")}}, []betterLeaksFinding{projected})
			if errors.Is(err, errBetterLeaksUnrewritable) != tc.wantBlocked {
				t.Fatalf("incomplete coverage blocked=%t; want %t", err != nil, tc.wantBlocked)
			}
		})
	}
}

func TestMultipartProjectionRepair_ComponentCapStillAdmitsKnownRuleOccurrences(t *testing.T) {
	finding := report.Finding{RuleID: "github-pat", Match: report.Match{Value: "x"}, Location: report.Location{StartLine: 1, EndLine: 1, StartColumn: 1, EndColumn: 1}}
	for i := 0; i < maxBetterLeaksProjectedComponents; i++ {
		finding.ComponentSets = append(finding.ComponentSets, report.ComponentSet{Components: []report.ComponentFinding{{RuleID: fmt.Sprintf("component-%d", i), Match: finding.Match, Location: finding.Location}}})
	}
	finding.ComponentSets = append(finding.ComponentSets, finding.ComponentSets[0])
	projected, err := projectBetterLeaksFinding(finding, "messages[0].parts[0]", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	defer releaseBetterLeaksOccurrenceBytes([]betterLeaksFinding{projected})
	if len(projected.occurrences) != maxBetterLeaksProjectedComponents+2 {
		t.Fatal("component metadata cap dropped a known rule occurrence")
	}
	if err := validateBetterLeaksRedactionEligibility([]LogicalFragment{{Location: projected.Location, Raw: []byte("x")}}, []betterLeaksFinding{projected}); err != nil {
		t.Fatal("complete known-rule projection blocked at metadata cap")
	}
}

func TestMultipartProjectionRepair_RealScannerTruncationBlocksClonePublication(t *testing.T) {
	guard, services := newBetterLeaksActionGuard(t, ActionRedact, 0, 0)
	generation := services.Capability.(*GenerationServices)
	scanner, err := blscan.New(&blconfig.Config{Rules: []blconfig.Rule{
		{ID: "github-pat", Regex: `ghp_[A-Za-z0-9]{36}`, Components: []blconfig.Component{{RuleID: "github-fine-grained-pat"}}},
		{ID: "github-fine-grained-pat", Regex: `companion-[0-9]{3}`, SkipReport: true},
	}}, blscan.WithWorkers(1), blscan.WithRegexEngine(blregexp.Stdlib{}))
	if err != nil {
		t.Fatal(err)
	}
	generation.betterLeaks.scanner = scanner
	text := adapterGitHubToken + "\n"
	for i := 0; i < 101; i++ {
		text += fmt.Sprintf("companion-%03d\n", i)
	}
	var upstream report.Finding
	_, err = scanner.Scan(context.Background(), betterLeaksLogicalFragmentSource{text: text}, func(f report.Finding) error { upstream = f; return nil })
	if err != nil || !upstream.ComponentSetsTruncated || len(upstream.ComponentSets) != 100 {
		t.Fatal("fixture did not trigger pinned scanner component truncation")
	}
	call := betterLeaksTextCall(text)
	call.Messages[0].Parts = append(call.Messages[0].Parts, lipapi.TextPart(adapterGitHubToken))
	// The unrelated candidate would successfully redact on its own.
	services.MatcherResolver = staticResolver{m: newExactStub(adapterGitHubToken, "REQUEST_CREDENTIAL", sdk.SourceCategoryRequestCred)}
	before := lipapi.CloneCall(call)
	decision, err := guard.Evaluate(context.Background(), &call, sdk.Meta{}, services)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != sdk.OutcomeBlock || decision.FailureKind != FailureKindUnrewritableDetectedSecret || decision.MutationCount != 0 {
		fatalDecisionSummary(t, decision)
	}
	if !reflect.DeepEqual(before, call) {
		t.Fatal("incomplete multipart redaction published a clone")
	}
	if err := decision.Validate(); err != nil {
		t.Fatal(err)
	}
	assertBetterLeaksDecisionSafe(t, decision)
}

func TestMultipartProjectionRepair_LogFailureRetainsIndependentFindingsAndLimit(t *testing.T) {
	for _, category := range []sdk.SourceCategory{sdk.SourceCategoryProxyEnv, sdk.SourceCategoryRequestCred} {
		t.Run(string(category), func(t *testing.T) {
			for _, limited := range []bool{false, true} {
				text := "GITHUB_TOKEN=" + adapterGitHubToken + " independent-secret"
				limit := 0
				if limited {
					limit = len(text) * 2
				}
				guard, services := newBetterLeaksActionGuard(t, ActionLog, limit, 1)
				services.MatcherResolver = staticResolver{m: newExactStub("independent-secret", "EXACT_CANARY", category)}
				call := betterLeaksTextCall(text)
				call.Messages[0].Parts = append(call.Messages[0].Parts, lipapi.TextPart(text))
				if limited {
					call.Messages[0].Parts = append(call.Messages[0].Parts, lipapi.TextPart(strings.Repeat("x", 20)))
				}
				before := lipapi.CloneCall(call)
				decision, err := guard.Evaluate(context.Background(), &call, sdk.Meta{}, services)
				if err != nil {
					t.Fatal(err)
				}
				wantKind, wantReason := sdk.FailureKindDetectorFailure, "betterleaks scan failed"
				if limited {
					wantKind, wantReason = FailureKindScanLimit, "scan_max_bytes exceeded; betterleaks scan failed"
				}
				if decision.Outcome != sdk.OutcomeLog || decision.FailureKind != wantKind || decision.ScanLimitHit != limited || decision.FailureReason != wantReason {
					fatalDecisionSummary(t, decision)
				}
				count := 0
				for _, f := range decision.Findings {
					if f.SecretRefName == "EXACT_CANARY" && f.SourceCategory == category {
						count++
					}
				}
				if count != 2 {
					t.Fatalf("independent exact findings=%d; want 2 admitted locations", count)
				}
				if !reflect.DeepEqual(before, call) {
					t.Fatal("log failure mutated call")
				}
				if err := decision.Validate(); err != nil {
					t.Fatal(err)
				}
				assertBetterLeaksDecisionSafe(t, decision)
			}
		})
	}
}
