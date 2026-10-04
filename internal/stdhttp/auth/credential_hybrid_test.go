package auth

import (
	"context"
	"strings"
	"testing"

	feature "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

func TestRequestCredentialHybridOverlapUsesConcreteOccurrences(t *testing.T) {
	const token = "ghp_" + "aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"
	m := newExactCredentialMatcher(token, "accepted-key")
	generation, err := feature.BuildGenerationServices(feature.DetectorPolicy{BetterLeaks: feature.BetterLeaksPolicy{
		Enabled: true, MinimumConfidence: "medium", Workers: 1, MaxFindings: 256, IsolateRules: []string{"github-pat"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := &lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(strings.Repeat("GITHUB_TOKEN="+token+" ", 2))}}}}
	decision, err := feature.NewGuard(feature.Config{Action: feature.ActionBlock, ScanMaxBytes: feature.DefaultScanMaxBytes}).Evaluate(t.Context(), call, sdk.Meta{}, sdk.Services{
		MatcherResolver: credentialTestResolver{m}, Capability: generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != sdk.OutcomeBlock || len(decision.Findings) != 1 || decision.Findings[0].OccurrenceCount != 2 || decision.Findings[0].SecretRefName != "accepted-key" || decision.Findings[0].RuleID != "github-pat" {
		t.Fatalf("overlap decision: outcome=%s findings=%+v", decision.Outcome, decision.Findings)
	}
}

type credentialTestResolver struct{ matcher sdk.Matcher }

func (r credentialTestResolver) Resolve(context.Context) (sdk.Matcher, error) { return r.matcher, nil }
