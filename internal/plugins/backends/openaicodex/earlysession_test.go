package openaicodex

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestApplyEarlySessionVerbosityBump_inWindowNoExplicitForcesHigh(t *testing.T) {
	t.Parallel()
	cfg := Config{EarlySessionVerbosityBumpTurns: 5}
	call := lipapi.Call{Options: lipapi.GenerationOptions{}}
	for turn := 1; turn <= 5; turn++ {
		payload := Payload{}
		applyEarlySessionVerbosityBump(&payload, call, cfg, turn)
		if payload.Text == nil || payload.Text.Verbosity != lipapi.VerbosityHigh {
			t.Fatalf("turn %d: expected forced high, got %+v", turn, payload.Text)
		}
	}
	payload := Payload{}
	applyEarlySessionVerbosityBump(&payload, call, cfg, 6)
	if payload.Text != nil {
		t.Fatalf("turn 6: expected no forced verbosity, got %+v", payload.Text)
	}
}

func TestApplyEarlySessionVerbosityBump_preservesExistingTextSpec(t *testing.T) {
	t.Parallel()
	cfg := Config{EarlySessionVerbosityBumpTurns: 1}
	existing := &textSpec{Verbosity: lipapi.VerbosityMedium}
	payload := Payload{Text: existing}
	call := lipapi.Call{Options: lipapi.GenerationOptions{}}
	applyEarlySessionVerbosityBump(&payload, call, cfg, 1)
	if payload.Text != existing {
		t.Fatal("bump must mutate existing textSpec in place")
	}
	if payload.Text.Verbosity != lipapi.VerbosityHigh {
		t.Fatalf("verbosity = %q, want high", payload.Text.Verbosity)
	}
}

func TestApplyEarlySessionVerbosityBump_explicitPerRequestWins(t *testing.T) {
	t.Parallel()
	cfg := Config{EarlySessionVerbosityBumpTurns: 1}
	existing := &textSpec{Verbosity: lipapi.VerbosityLow}
	payload := Payload{Text: existing}
	call := lipapi.Call{Options: lipapi.GenerationOptions{Verbosity: lipapi.VerbosityLow}}
	applyEarlySessionVerbosityBump(&payload, call, cfg, 1)
	if payload.Text != existing || payload.Text.Verbosity != lipapi.VerbosityLow {
		t.Fatalf("explicit verbosity must win, got %+v", payload.Text)
	}
}

func TestApplyEarlySessionVerbosityBump_pastWindowNoBump(t *testing.T) {
	t.Parallel()
	cfg := Config{EarlySessionVerbosityBumpTurns: 2}
	call := lipapi.Call{Options: lipapi.GenerationOptions{}}
	payload := Payload{}
	applyEarlySessionVerbosityBump(&payload, call, cfg, 3)
	if payload.Text != nil {
		t.Fatalf("past window must not set Text, got %+v", payload.Text)
	}
}

func TestApplyEarlySessionVerbosityBump_disabledNoBump(t *testing.T) {
	t.Parallel()
	cfg := Config{EarlySessionVerbosityBumpDisabled: true, EarlySessionVerbosityBumpTurns: 1}
	payload := Payload{}
	call := lipapi.Call{Options: lipapi.GenerationOptions{}}
	applyEarlySessionVerbosityBump(&payload, call, cfg, 1)
	if payload.Text != nil {
		t.Fatalf("disabled must not set Text, got %+v", payload.Text)
	}
}

func TestApplyEarlySessionVerbosityBump_zeroTurnNoBump(t *testing.T) {
	t.Parallel()
	cfg := Config{EarlySessionVerbosityBumpTurns: 5}
	payload := Payload{}
	call := lipapi.Call{Options: lipapi.GenerationOptions{}}
	applyEarlySessionVerbosityBump(&payload, call, cfg, 0)
	if payload.Text != nil {
		t.Fatalf("turn 0 must not set Text, got %+v", payload.Text)
	}
}

func TestApplyEarlySessionVerbosityBump_zeroOrNegativeTurnsDefaultsToFive(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{Options: lipapi.GenerationOptions{}}
	cases := []struct {
		name  string
		turns int
	}{
		{"zero", 0},
		{"negative", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{EarlySessionVerbosityBumpTurns: tc.turns}
			for turn := 1; turn <= 5; turn++ {
				var p Payload
				applyEarlySessionVerbosityBump(&p, call, cfg, turn)
				if p.Text == nil || p.Text.Verbosity != lipapi.VerbosityHigh {
					t.Fatalf("turn %d expected high, got %+v", turn, p.Text)
				}
			}
			var p Payload
			applyEarlySessionVerbosityBump(&p, call, cfg, 6)
			if p.Text != nil {
				t.Fatalf("turn 6 expected no bump, got %+v", p.Text)
			}
		})
	}
}

func TestPrepareCodexOpenEnv_earlySessionBumpAppliesAndReverts(t *testing.T) {
	t.Parallel()
	turns := newSessionTurnCounter(time.Hour, 64)
	cfg := Config{
		BaseURL:                        "https://example.test/backend-api/codex",
		EarlySessionVerbosityBumpTurns: 5,
	}
	policy := newDowngradePolicy(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4"}}
	mkCall := func() lipapi.Call {
		return lipapi.Call{
			Session:  lipapi.SessionRef{ContinuityKey: "conv-bump"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
		}
	}
	for turn := 1; turn <= 5; turn++ {
		env, err := prepareCodexOpenEnv(context.Background(), &cfg, mkCall(), cand, policy, turns)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		if env.payload.Text == nil || env.payload.Text.Verbosity != lipapi.VerbosityHigh {
			t.Fatalf("turn %d: expected forced high, got %+v", turn, env.payload.Text)
		}
		if !env.turnReserved {
			t.Fatalf("turn %d: expected reservation", turn)
		}
	}
	env, err := prepareCodexOpenEnv(context.Background(), &cfg, mkCall(), cand, policy, turns)
	if err != nil {
		t.Fatal(err)
	}
	if env.payload.Text != nil {
		t.Fatalf("turn 6: expected no forced verbosity, got %+v", env.payload.Text)
	}
}

func TestPrepareCodexOpenEnv_explicitVerbosityWinsOnTurn1(t *testing.T) {
	t.Parallel()
	turns := newSessionTurnCounter(time.Hour, 64)
	cfg := Config{
		BaseURL:                        "https://example.test/backend-api/codex",
		EarlySessionVerbosityBumpTurns: 5,
	}
	policy := newDowngradePolicy(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4"}}
	call := lipapi.Call{
		Session:  lipapi.SessionRef{ContinuityKey: "conv-explicit"},
		Options:  lipapi.GenerationOptions{Verbosity: lipapi.VerbosityLow},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	env, err := prepareCodexOpenEnv(context.Background(), &cfg, call, cand, policy, turns)
	if err != nil {
		t.Fatal(err)
	}
	if env.payload.Text == nil || env.payload.Text.Verbosity != lipapi.VerbosityLow {
		t.Fatalf("explicit low must win on turn 1, got %+v", env.payload.Text)
	}
	if !env.turnReserved {
		t.Fatal("explicit verbosity turn must still reserve a shared turn slot")
	}
}

func TestPrepareCodexOpenEnv_releaseRestoresEarlyWindowSlot(t *testing.T) {
	t.Parallel()
	turns := newSessionTurnCounter(time.Hour, 64)
	cfg := Config{
		BaseURL:                        "https://example.test/backend-api/codex",
		EarlySessionVerbosityBumpTurns: 1,
	}
	policy := newDowngradePolicy(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4"}}
	call := lipapi.Call{
		Session:  lipapi.SessionRef{ContinuityKey: "conv-release"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	env, err := prepareCodexOpenEnv(context.Background(), &cfg, call, cand, policy, turns)
	if err != nil {
		t.Fatal(err)
	}
	if env.payload.Text == nil || env.payload.Text.Verbosity != lipapi.VerbosityHigh {
		t.Fatalf("turn 1 expected high, got %+v", env.payload.Text)
	}
	env.releaseVerbosityTurn()
	env2, err := prepareCodexOpenEnv(context.Background(), &cfg, call, cand, policy, turns)
	if err != nil {
		t.Fatal(err)
	}
	if env2.payload.Text == nil || env2.payload.Text.Verbosity != lipapi.VerbosityHigh {
		t.Fatalf("after release, turn 1 should bump again, got %+v", env2.payload.Text)
	}
}

func TestPrepareCodexOpenEnv_keepsTurnCounterOnContinuityKeyWhenAuthoritativeSessionChanges(t *testing.T) {
	t.Parallel()
	turns := newSessionTurnCounter(time.Hour, 64)
	cfg := Config{
		BaseURL:                        "https://example.test/backend-api/codex",
		EarlySessionVerbosityBumpTurns: 1,
	}
	policy := newDowngradePolicy(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4"}}
	mkCall := func(authoritativeID string) lipapi.Call {
		return lipapi.Call{
			Session: lipapi.SessionRef{
				ContinuityKey:          "shared-continuity",
				AuthoritativeSessionID: authoritativeID,
			},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
		}
	}

	envA, err := prepareCodexOpenEnv(context.Background(), &cfg, mkCall("auth-1"), cand, policy, turns)
	if err != nil {
		t.Fatal(err)
	}
	if envA.payload.Text == nil || envA.payload.Text.Verbosity != lipapi.VerbosityHigh {
		t.Fatalf("session auth-1 turn 1 expected high, got %+v", envA.payload.Text)
	}

	envB, err := prepareCodexOpenEnv(context.Background(), &cfg, mkCall("auth-2"), cand, policy, turns)
	if err != nil {
		t.Fatal(err)
	}
	if envB.payload.Text != nil {
		t.Fatalf("authoritative session change should not reset the continuity turn counter, got %+v", envB.payload.Text)
	}

	envA.releaseVerbosityTurn()
	envB.releaseVerbosityTurn()
}
