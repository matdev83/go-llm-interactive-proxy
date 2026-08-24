package lipruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type stubMatcherForAdapt struct{ redacted string }

func (s stubMatcherForAdapt) ScanBytes(_ context.Context, _ []byte) ([]sdk.Finding, error) {
	return nil, nil
}

func (s stubMatcherForAdapt) ScanString(_ context.Context, _ string) ([]sdk.Finding, error) {
	return nil, nil
}

func (s stubMatcherForAdapt) RedactBytes(_ context.Context, b []byte) ([]byte, []sdk.Finding, error) {
	if s.redacted != "" {
		return []byte(strings.ReplaceAll(string(b), "SECRET", s.redacted)), nil, nil
	}
	return b, nil, nil
}

func (s stubMatcherForAdapt) RedactString(_ context.Context, in string) (string, []sdk.Finding, error) {
	if s.redacted != "" {
		return strings.ReplaceAll(in, "SECRET", s.redacted), nil, nil
	}
	return in, nil, nil
}

type stubResolverForAdapt struct{ m sdk.Matcher }

func (s stubResolverForAdapt) Resolve(_ context.Context) (sdk.Matcher, error) { return s.m, nil }

type recordingAdaptPolicy struct {
	version  string
	action   EgressAction
	received *EgressInput
}

func (r *recordingAdaptPolicy) Decide(_ context.Context, in EgressInput) (EgressDecision, error) {
	cp := in
	r.received = &cp
	// Mutate returned scope to test defensive clone.
	mut := in.Scope()
	mut.Roles = append(mut.Roles, "evil")
	mut.SafeClaims["evil"] = "1"
	mut.PolicyLabels["evil"] = "1"
	return EgressDecision{Action: r.action, PolicyVersion: r.version}, nil
}

func fullScopeForAdapt() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectHuman,
		PrincipalID:    scope.Known("user-123"),
		DisplayName:    scope.Known("Alice"),
		Roles:          []string{"admin", "editor"},
		SafeClaims:     map[string]string{"team": "core"},
		TenantID:       scope.Known("t-1"),
		OrganizationID: scope.Known("org-1"),
		WorkspaceID:    scope.Known("ws-1"),
		ProjectID:      scope.Known("proj-1"),
		DepartmentID:   scope.Known("dept-1"),
		CostCenterID:   scope.Known("cc-1"),
		PolicyLabels:   map[string]string{"tier": "gold"},
		Origin:         scope.OriginClient,
	}
}

func TestAdaptReasoningCompressionOptions_MapsActionsAndPolicyVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		pubAction  EgressAction
		wantAction reasoningpreservation.EgressAction
		version    string
	}{
		{"allow", EgressAllow, reasoningpreservation.EgressAllow, "vAllow"},
		{"deny", EgressDeny, reasoningpreservation.EgressDeny, "vDeny"},
		{"redact", EgressRedactThenAllow, reasoningpreservation.EgressRedactThenAllow, "vRedact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &recordingAdaptPolicy{version: tc.version, action: tc.pubAction}
			pub := ReasoningCompressionOptions{
				EgressPolicies:  map[string]EgressPolicy{"ref": rec},
				MatcherResolver: stubResolverForAdapt{m: stubMatcherForAdapt{redacted: "REDACTED"}},
			}
			adapted := adaptReasoningCompressionOptions(pub)
			if len(adapted.EgressPolicies) != 1 {
				t.Fatalf("adapted policies len=%d want 1", len(adapted.EgressPolicies))
			}
			pol, ok := adapted.EgressPolicies["ref"]
			if !ok || pol == nil {
				t.Fatal("adapted policy missing")
			}
			// Build internal input with full scope.
			internalScope := fullScopeForAdapt()
			in := reasoningpreservation.CompressionEgressInput{
				Route:       "test-route",
				Purpose:     reasoningpreservation.EgressPurposeReasoningSemanticCompression,
				SourceClass: reasoningpreservation.EgressSourceClassSemanticText,
				Principal:   reasoningpreservation.NewEgressPrincipalScopeView(internalScope),
			}
			dec, err := pol.Decide(context.Background(), in)
			if err != nil {
				t.Fatalf("Decide err: %v", err)
			}
			if dec.Action != tc.wantAction {
				t.Fatalf("action: got %v want %v", dec.Action, tc.wantAction)
			}
			if dec.PolicyVersion != tc.version {
				t.Fatalf("version: got %q want %q", dec.PolicyVersion, tc.version)
			}
			// Verify policy received full scope fields.
			if rec.received == nil {
				t.Fatal("policy did not receive input")
			}
			gotScope := rec.received.Scope()
			if !gotScope.PrincipalID.Equal(internalScope.PrincipalID) {
				t.Fatalf("PrincipalID mismatch")
			}
			if !gotScope.TenantID.Equal(internalScope.TenantID) {
				t.Fatalf("TenantID mismatch")
			}
			if !gotScope.OrganizationID.Equal(internalScope.OrganizationID) {
				t.Fatalf("OrganizationID mismatch")
			}
			if !gotScope.WorkspaceID.Equal(internalScope.WorkspaceID) {
				t.Fatalf("WorkspaceID mismatch")
			}
			if !gotScope.ProjectID.Equal(internalScope.ProjectID) {
				t.Fatalf("ProjectID mismatch")
			}
			if !gotScope.DepartmentID.Equal(internalScope.DepartmentID) {
				t.Fatalf("DepartmentID mismatch")
			}
			if !gotScope.CostCenterID.Equal(internalScope.CostCenterID) {
				t.Fatalf("CostCenterID mismatch")
			}
			if len(gotScope.Roles) != len(internalScope.Roles) {
				t.Fatalf("Roles len mismatch")
			}
			if gotScope.SafeClaims["team"] != "core" {
				t.Fatalf("SafeClaims mismatch")
			}
			if gotScope.PolicyLabels["tier"] != "gold" {
				t.Fatalf("PolicyLabels mismatch")
			}
			if gotScope.Origin != scope.OriginClient {
				t.Fatalf("Origin mismatch")
			}
			// Verify defensive clone: mutation inside policy did not affect internal source
			// and second Scope() call does not see mutation.
			again := rec.received.Scope()
			if len(again.Roles) != len(internalScope.Roles) {
				t.Fatalf("Scope() second call should not see mutated Roles, got %v", again.Roles)
			}
			if _, ok := again.SafeClaims["evil"]; ok {
				t.Fatal("Scope() second call leaked mutated SafeClaims")
			}
			// Also verify internal source not mutated: original internalScope should remain unchanged,
			// and the internal input's Principal.Scope() should still be clean.
			srcAgain := in.Principal.Scope()
			if len(srcAgain.Roles) != len(internalScope.Roles) {
				t.Fatalf("internal source mutated via policy, got %v", srcAgain.Roles)
			}
			if _, ok := srcAgain.SafeClaims["evil"]; ok {
				t.Fatal("internal source mutated via policy SafeClaims")
			}
		})
	}
}

func TestAdaptReasoningCompressionOptions_TypedNilPolicyAndResolverFailClosed(t *testing.T) {
	t.Parallel()
	var nilPolicy *recordingAdaptPolicy
	var nilResolver *stubResolverForAdapt
	pub := ReasoningCompressionOptions{
		EgressPolicies:  map[string]EgressPolicy{"ref": nilPolicy},
		MatcherResolver: nilResolver,
	}
	adapted := adaptReasoningCompressionOptions(pub)
	if adapted.MatcherResolver != nil {
		t.Fatal("typed nil resolver should adapt to nil")
	}
	if len(adapted.EgressPolicies) != 1 {
		t.Fatalf("adapted policies len=%d want 1", len(adapted.EgressPolicies))
	}
	if p, ok := adapted.EgressPolicies["ref"]; !ok {
		t.Fatal("missing ref")
	} else if p != nil {
		t.Fatal("typed nil policy should adapt to nil entry, not adapter")
	}
	// Ensure Decide on a typed-nil adapter would not panic (but we have nil entry, so no adapter)
	// Also test map with mixed nil and valid
	pub2 := ReasoningCompressionOptions{
		EgressPolicies: map[string]EgressPolicy{
			"nil-ref": nilPolicy,
			"valid":   &recordingAdaptPolicy{version: "v1", action: EgressAllow},
		},
		MatcherResolver: stubResolverForAdapt{m: stubMatcherForAdapt{}},
	}
	adapted2 := adaptReasoningCompressionOptions(pub2)
	if len(adapted2.EgressPolicies) != 2 {
		t.Fatalf("len=%d", len(adapted2.EgressPolicies))
	}
	if adapted2.EgressPolicies["nil-ref"] != nil {
		t.Fatal("nil-ref should be nil")
	}
	if adapted2.EgressPolicies["valid"] == nil {
		t.Fatal("valid should be non-nil")
	}
}

func TestAdaptReasoningCompressionOptions_TrimsAndIgnoresEmptyKey(t *testing.T) {
	t.Parallel()
	pub := ReasoningCompressionOptions{
		EgressPolicies: map[string]EgressPolicy{
			"  ":  &recordingAdaptPolicy{version: "v1", action: EgressAllow},
			" a ": &recordingAdaptPolicy{version: "v1", action: EgressAllow},
		},
		MatcherResolver: stubResolverForAdapt{m: stubMatcherForAdapt{}},
	}
	adapted := adaptReasoningCompressionOptions(pub)
	if len(adapted.EgressPolicies) != 1 {
		t.Fatalf("empty key should be ignored, got %d", len(adapted.EgressPolicies))
	}
	if _, ok := adapted.EgressPolicies["a"]; !ok {
		t.Fatalf("trimmed key 'a' missing: %+v", adapted.EgressPolicies)
	}
}

func TestEgressPolicyAdapter_RedactSuppliesSanitizer(t *testing.T) {
	t.Parallel()
	rec := &recordingAdaptPolicy{version: "v1", action: EgressRedactThenAllow}
	pub := ReasoningCompressionOptions{
		EgressPolicies:  map[string]EgressPolicy{"ref": rec},
		MatcherResolver: stubResolverForAdapt{m: stubMatcherForAdapt{redacted: "REDACTED"}},
	}
	adapted := adaptReasoningCompressionOptions(pub)
	pol := adapted.EgressPolicies["ref"]
	in := reasoningpreservation.CompressionEgressInput{
		Route:       "r",
		Purpose:     reasoningpreservation.EgressPurposeReasoningSemanticCompression,
		SourceClass: reasoningpreservation.EgressSourceClassSemanticText,
		Principal:   reasoningpreservation.NewEgressPrincipalScopeView(fullScopeForAdapt()),
	}
	dec, err := pol.Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if dec.Action != reasoningpreservation.EgressRedactThenAllow {
		t.Fatalf("want redact, got %v", dec.Action)
	}
	if dec.Sanitizer == nil {
		t.Fatal("redact should supply sanitizer from resolver")
	}
	// Verify sanitizer actually redacts
	out, err := dec.Sanitizer.SanitizeText(context.Background(), "has SECRET here")
	if err != nil {
		t.Fatalf("SanitizeText err: %v", err)
	}
	if out != "has REDACTED here" {
		t.Fatalf("SanitizeText got %q want %q", out, "has REDACTED here")
	}
	// With typed nil resolver, redact should not supply sanitizer but still return action (runtime will fail closed)
	pubNilRes := ReasoningCompressionOptions{
		EgressPolicies:  map[string]EgressPolicy{"ref": rec},
		MatcherResolver: (*stubResolverForAdapt)(nil),
	}
	adaptedNil := adaptReasoningCompressionOptions(pubNilRes)
	polNil := adaptedNil.EgressPolicies["ref"]
	dec2, err := polNil.Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if dec2.Sanitizer != nil {
		t.Fatal("typed nil resolver should not supply sanitizer")
	}
}
