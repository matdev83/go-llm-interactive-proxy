package featurehost_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkfeaturehost "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/featurehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/reasoninghost"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type unknownMockBinding struct{}

func (u unknownMockBinding) HostBindingID() string {
	return "custom-unsupported"
}

func (u unknownMockBinding) ValidateHostBinding() error {
	return nil
}

type badIDReasoningBinding struct {
	reasoninghost.Binding
}

func (b badIDReasoningBinding) HostBindingID() string {
	return "unexpected-reasoning-id"
}

func (b badIDReasoningBinding) ValidateHostBinding() error {
	return nil
}

type recordingHostPolicy struct {
	decision reasoninghost.EgressDecision
	received *reasoninghost.EgressInput
}

func (r *recordingHostPolicy) Decide(_ context.Context, in reasoninghost.EgressInput) (reasoninghost.EgressDecision, error) {
	cp := in
	r.received = &cp
	return r.decision, nil
}

type stubSecretResolver struct {
	matcher sdk.Matcher
}

func (s stubSecretResolver) Resolve(_ context.Context) (sdk.Matcher, error) {
	return s.matcher, nil
}

func TestBindings_UnknownBindingType_FailsBeforeServing(t *testing.T) {
	t.Parallel()

	in := featurehost.ProcessInput{
		Logger: slog.Default(),
		HostRegistrations: []sdkfeaturehost.Registration{
			{Binding: unknownMockBinding{}},
		},
	}

	_, err := featurehost.NewProcess(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for unknown binding type, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported host binding") {
		t.Fatalf("expected unsupported host binding error, got: %v", err)
	}
}

func TestBindings_UnknownBindingID_FailsBeforeServing(t *testing.T) {
	t.Parallel()

	in := featurehost.ProcessInput{
		Logger: slog.Default(),
		HostRegistrations: []sdkfeaturehost.Registration{
			{Binding: badIDReasoningBinding{}},
		},
	}

	_, err := featurehost.NewProcess(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for unknown binding ID, got nil")
	}
	if !strings.Contains(err.Error(), "unknown binding ID") && !strings.Contains(err.Error(), "unsupported host binding") {
		t.Fatalf("expected unknown/unsupported binding error, got: %v", err)
	}
}

func TestBindings_DuplicateSemanticBinding_FailsBeforeServing(t *testing.T) {
	t.Parallel()

	in := featurehost.ProcessInput{
		Logger: slog.Default(),
		HostRegistrations: []sdkfeaturehost.Registration{
			{Binding: &reasoninghost.Binding{}},
			{Binding: &reasoninghost.Binding{}},
		},
	}

	_, err := featurehost.NewProcess(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for duplicate semantic binding, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate binding error, got: %v", err)
	}
}

func TestBindings_InvalidRegistration_FailsBeforeServing(t *testing.T) {
	t.Parallel()

	in := featurehost.ProcessInput{
		Logger: slog.Default(),
		HostRegistrations: []sdkfeaturehost.Registration{
			{Binding: nil},
		},
	}

	_, err := featurehost.NewProcess(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for nil registration binding, got nil")
	}
	if !errors.Is(err, sdkfeaturehost.ErrNilBinding) {
		t.Fatalf("expected ErrNilBinding, got: %v", err)
	}
}

func TestBindings_ValidReasoningHostBinding_SucceedsAndMapsEgress(t *testing.T) {
	t.Parallel()

	recPolicy := &recordingHostPolicy{
		decision: reasoninghost.EgressDecision{
			Action:        reasoninghost.EgressAllow,
			PolicyVersion: "v2-test",
		},
	}
	resolver := stubSecretResolver{}

	binding := &reasoninghost.Binding{
		EgressPolicies: map[string]reasoninghost.EgressPolicy{
			"policy-test": recPolicy,
		},
		MatcherResolver: resolver,
	}

	in := featurehost.ProcessInput{
		Logger: slog.Default(),
		HostRegistrations: []sdkfeaturehost.Registration{
			binding.Registration(),
		},
	}

	rt, err := featurehost.NewProcess(context.Background(), in)
	if err != nil {
		t.Fatalf("NewProcess with valid reasoninghost binding failed: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	// Compile generation and verify reasoning composition received the adapted policy
	genIn := featurehost.GenerationInput{
		MergeSurface: featurebundle.GeneratedMergeSurface{
			Frozen: lipfeature.FrozenPlaneSet{},
		},
	}
	out, err := rt.CompileGeneration(context.Background(), genIn)
	if err != nil {
		t.Fatalf("CompileGeneration failed: %v", err)
	}
	if out.Bundle.PlaneSet.IsZero() && genIn.MergeSurface.Frozen.IsZero() {
		// Plane is populated or merged
	}

	// Verify the bound reasoning options on Runtime
	boundOpts := rt.BoundReasoningOptions()
	if boundOpts.MatcherResolver == nil {
		t.Fatal("expected MatcherResolver to be populated on bound reasoning options")
	}
	adaptedPolicy, exists := boundOpts.EgressPolicies["policy-test"]
	if !exists {
		t.Fatal("expected policy-test to exist in bound reasoning EgressPolicies")
	}

	// Test decision through the adapted policy
	internalInput := reasoningpreservation.CompressionEgressInput{
		Route:       "route-A",
		Purpose:     "audit",
		SourceClass: "prompt",
		Principal:   reasoningpreservation.NewEgressPrincipalView("user-42"),
	}
	dec, err := adaptedPolicy.Decide(context.Background(), internalInput)
	if err != nil {
		t.Fatalf("adaptedPolicy.Decide failed: %v", err)
	}
	if dec.Action != reasoningpreservation.EgressAllow {
		t.Fatalf("expected EgressAllow, got %v", dec.Action)
	}
	if dec.PolicyVersion != "v2-test" {
		t.Fatalf("expected PolicyVersion 'v2-test', got %q", dec.PolicyVersion)
	}
	if recPolicy.received == nil || recPolicy.received.Route != "route-A" {
		t.Fatalf("expected host policy to receive route-A, got: %+v", recPolicy.received)
	}
}
