package openaicodex

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestApplyMidSessionVerbosityBump_periodicHitsForceHigh(t *testing.T) {
	t.Parallel()
	cfg := Config{MidSessionVerbosityBumpFrequency: 10}
	call := lipapi.Call{Options: lipapi.GenerationOptions{}}
	for turn := 1; turn <= 30; turn++ {
		var payload Payload
		applyMidSessionVerbosityBump(&payload, call, cfg, turn)
		wantHigh := turn%10 == 0
		if wantHigh {
			if payload.Text == nil || payload.Text.Verbosity != lipapi.VerbosityHigh {
				t.Fatalf("turn %d: expected forced high, got %+v", turn, payload.Text)
			}
		} else if payload.Text != nil {
			t.Fatalf("turn %d: expected no forced verbosity, got %+v", turn, payload.Text)
		}
	}
}

func TestApplyMidSessionVerbosityBump_explicitPerRequestWins(t *testing.T) {
	t.Parallel()
	cfg := Config{MidSessionVerbosityBumpFrequency: 2}
	explicitCall := lipapi.Call{Options: lipapi.GenerationOptions{Verbosity: lipapi.VerbosityLow}}
	explicitPayload := Payload{Text: &textSpec{Verbosity: lipapi.VerbosityLow}}
	applyMidSessionVerbosityBump(&explicitPayload, explicitCall, cfg, 2)
	if explicitPayload.Text == nil || explicitPayload.Text.Verbosity != lipapi.VerbosityLow {
		t.Fatalf("explicit low must win on frequency turn, got %+v", explicitPayload.Text)
	}
	plainCall := lipapi.Call{Options: lipapi.GenerationOptions{}}
	nextPayload := Payload{}
	applyMidSessionVerbosityBump(&nextPayload, plainCall, cfg, 2)
	if nextPayload.Text == nil || nextPayload.Text.Verbosity != lipapi.VerbosityHigh {
		t.Fatalf("plain frequency turn 2 should force high, got %+v", nextPayload.Text)
	}
}

func TestApplyMidSessionVerbosityBump_disabledNoBump(t *testing.T) {
	t.Parallel()
	cfg := Config{MidSessionVerbosityBumpDisabled: true, MidSessionVerbosityBumpFrequency: 2}
	call := lipapi.Call{Options: lipapi.GenerationOptions{}}
	var payload Payload
	applyMidSessionVerbosityBump(&payload, call, cfg, 2)
	if payload.Text != nil {
		t.Fatalf("disabled frequency turn: expected no forced verbosity, got %+v", payload.Text)
	}
}

func TestApplyMidSessionVerbosityBump_zeroTurnNoBump(t *testing.T) {
	t.Parallel()
	cfg := Config{MidSessionVerbosityBumpFrequency: 2}
	call := lipapi.Call{Options: lipapi.GenerationOptions{}}
	var payload Payload
	applyMidSessionVerbosityBump(&payload, call, cfg, 0)
	if payload.Text != nil {
		t.Fatalf("turn 0 must not set Text, got %+v", payload.Text)
	}
}

func TestApplyMidSessionVerbosityBump_zeroOrNegativeFrequencyDefaultsToTen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		freq int
	}{
		{name: "zero", freq: 0},
		{name: "negative", freq: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{MidSessionVerbosityBumpFrequency: tc.freq}
			call := lipapi.Call{Options: lipapi.GenerationOptions{}}
			for turn := 1; turn <= 10; turn++ {
				var payload Payload
				applyMidSessionVerbosityBump(&payload, call, cfg, turn)
				if turn < 10 {
					if payload.Text != nil {
						t.Fatalf("turn %d: expected no forced verbosity, got %+v", turn, payload.Text)
					}
				} else if payload.Text == nil || payload.Text.Verbosity != lipapi.VerbosityHigh {
					t.Fatalf("turn 10: expected forced high, got %+v", payload.Text)
				}
			}
		})
	}
}

func TestValidateVerbosityBumpConfig_rejectsFrequencyAtOrBelowEarlyWindow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  Config
	}{
		{
			name: "equal to early window",
			cfg: Config{
				EarlySessionVerbosityBumpTurns:   10,
				MidSessionVerbosityBumpFrequency: 10,
			},
		},
		{
			name: "default frequency too low for larger early window",
			cfg: Config{
				EarlySessionVerbosityBumpTurns: 12,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateVerbosityBumpConfig(tc.cfg)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), "mid_session_verbosity_bump_frequency") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestValidateVerbosityBumpConfig_allowsDisabledMidSessionRegardlessOfFrequency(t *testing.T) {
	t.Parallel()
	cfg := Config{
		EarlySessionVerbosityBumpTurns:   15,
		MidSessionVerbosityBumpDisabled:  true,
		MidSessionVerbosityBumpFrequency: 10,
	}
	if err := validateVerbosityBumpConfig(cfg); err != nil {
		t.Fatalf("disabled mid-session config must pass validation, got %v", err)
	}
}

func TestPrepareCodexOpenEnv_midSessionBumpAppliesAtTurn10And20(t *testing.T) {
	t.Parallel()
	turns := newSessionTurnCounter(time.Hour, 64)
	cfg := Config{
		BaseURL:                           "https://example.test/backend-api/codex",
		EarlySessionVerbosityBumpDisabled: true,
		MidSessionVerbosityBumpFrequency:  10,
	}
	policy := newDowngradePolicy(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4"}}
	call := lipapi.Call{
		Session:  lipapi.SessionRef{ContinuityKey: "conv-mid"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	for turn := 1; turn <= 20; turn++ {
		env, err := prepareCodexOpenEnv(context.Background(), &cfg, call, cand, policy, turns)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		wantHigh := turn%10 == 0
		if wantHigh {
			if env.payload.Text == nil || env.payload.Text.Verbosity != lipapi.VerbosityHigh {
				t.Fatalf("turn %d: expected forced high, got %+v", turn, env.payload.Text)
			}
		} else if env.payload.Text != nil {
			t.Fatalf("turn %d: expected no forced verbosity, got %+v", turn, env.payload.Text)
		}
	}
}

func TestPrepareCodexOpenEnv_sharedCounterKeepsEarlyAndMidAligned(t *testing.T) {
	t.Parallel()
	turns := newSessionTurnCounter(time.Hour, 64)
	cfg := Config{
		BaseURL:                          "https://example.test/backend-api/codex",
		EarlySessionVerbosityBumpTurns:   5,
		MidSessionVerbosityBumpFrequency: 10,
	}
	policy := newDowngradePolicy(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4"}}
	call := lipapi.Call{
		Session:  lipapi.SessionRef{ContinuityKey: "conv-shared"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	for turn := 1; turn <= 10; turn++ {
		env, err := prepareCodexOpenEnv(context.Background(), &cfg, call, cand, policy, turns)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		wantHigh := turn <= 5 || turn == 10
		if wantHigh {
			if env.payload.Text == nil || env.payload.Text.Verbosity != lipapi.VerbosityHigh {
				t.Fatalf("turn %d: expected forced high, got %+v", turn, env.payload.Text)
			}
		} else if env.payload.Text != nil {
			t.Fatalf("turn %d: expected no forced verbosity, got %+v", turn, env.payload.Text)
		}
	}
}
