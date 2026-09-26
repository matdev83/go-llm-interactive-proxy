package configreload

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

func TestClassifySelfDefensePolicyFieldsReloadable(t *testing.T) {
	t.Parallel()

	active := &config.Config{}
	candidate := &config.Config{}
	disabled := false
	impossible := false
	authFailures := 8
	candidate.Access.SelfDefense.Enabled = &disabled
	candidate.Access.SelfDefense.ImpossiblePaths = &impossible
	candidate.Access.SelfDefense.Adaptive.AuthFailures = &authFailures
	candidate.Access.SelfDefense.Adaptive.Window = "30s"
	candidate.Access.SelfDefense.Adaptive.InitialQuarantine = "5m"
	candidate.Access.SelfDefense.Adaptive.MaxQuarantine = "4h"
	candidate.Access.SelfDefense.Adaptive.ExemptCIDRs = []string{"203.0.113.0/24"}

	changes, err := Classify(active, candidate)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := []string{
		"access.self_defense.adaptive.auth_failures",
		"access.self_defense.adaptive.exempt_cidrs",
		"access.self_defense.adaptive.initial_quarantine",
		"access.self_defense.adaptive.max_quarantine",
		"access.self_defense.adaptive.window",
		"access.self_defense.enabled",
		"access.self_defense.impossible_paths",
	}
	got := make([]string, 0, len(changes))
	for _, change := range changes {
		if change.Disposition != ChangeReloadable {
			t.Fatalf("change %q disposition = %q, want reloadable", change.Path, change.Disposition)
		}
		got = append(got, change.Path)
	}
	if len(got) != len(want) {
		t.Fatalf("reloadable changes = %v, want exactly %d paths %v", got, len(want), want)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("reloadable change set = %v, want exactly %v", got, want)
	}
}

func TestClassifySelfDefenseStateFieldsRestartRequired(t *testing.T) {
	t.Parallel()

	active := &config.Config{}
	candidate := &config.Config{}
	maxEntries := 200000
	candidate.Access.SelfDefense.Adaptive.StateTTL = "48h"
	candidate.Access.SelfDefense.Adaptive.MaxEntries = &maxEntries

	_, err := Classify(active, candidate)
	if err == nil {
		t.Fatal("Classify succeeded, want restart-required error")
	}
	restart, ok := err.(*RestartRequiredError)
	if !ok {
		t.Fatalf("error type = %T, want *RestartRequiredError", err)
	}
	for _, path := range []string{
		"access.self_defense.adaptive.state_ttl",
		"access.self_defense.adaptive.max_entries",
	} {
		if !slices.Contains(restart.RestartRequiredFields, path) {
			t.Errorf("restart fields %v missing %q", restart.RestartRequiredFields, path)
		}
	}
}

func TestClassifySelfDefenseMixedCandidateRejectsAtomically(t *testing.T) {
	t.Parallel()

	active := &config.Config{}
	candidate := &config.Config{}
	disabled := false
	candidate.Access.SelfDefense.Enabled = &disabled
	candidate.Access.SelfDefense.Adaptive.StateTTL = "48h"

	changes, err := Classify(active, candidate)
	if err == nil {
		t.Fatalf("mixed candidate must reject, got changes=%v", changes)
	}
	if changes != nil {
		t.Fatalf("mixed reject must not publish reloadable changes, got %v", changes)
	}
	restart, ok := err.(*RestartRequiredError)
	if !ok {
		t.Fatalf("error type = %T, want *RestartRequiredError", err)
	}
	if !slices.Contains(restart.RestartRequiredFields, "access.self_defense.adaptive.state_ttl") {
		t.Fatalf("want state_ttl restart-required, got %v", restart.RestartRequiredFields)
	}
	if slices.Contains(restart.RestartRequiredFields, "access.self_defense.enabled") {
		t.Fatalf("reloadable enabled published as restart-required: %v", restart.RestartRequiredFields)
	}
}

func TestClassifySelfDefenseNoop(t *testing.T) {
	t.Parallel()

	active := &config.Config{}
	candidate := &config.Config{}

	changes, err := Classify(active, candidate)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if changes != nil {
		t.Fatalf("noop must return nil changes, got %v", changes)
	}
}
