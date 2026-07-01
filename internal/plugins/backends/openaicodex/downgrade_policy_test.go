package openaicodex

import "testing"

func TestDowngradePolicy_customSourceTarget(t *testing.T) {
	t.Parallel()
	p := newDowngradePolicy(Config{
		GPT55DowngradeSourceModel: "custom-src",
		GPT55DowngradeTargetModel: "custom-dst",
	})
	if got := p.modelForPlan("custom-src", "free"); got != "custom-dst" {
		t.Fatalf("modelForPlan = %q, want custom-dst", got)
	}
	if got := p.modelForPlan("gpt-5.5", "free"); got != "gpt-5.5" {
		t.Fatalf("default source unchanged = %q, want gpt-5.5", got)
	}
}

func TestDowngradePolicy_disabled(t *testing.T) {
	t.Parallel()
	p := newDowngradePolicy(Config{GPT55DowngradeDisabled: true})
	if got := p.modelForPlan("gpt-5.5", "free"); got != "gpt-5.5" {
		t.Fatalf("disabled modelForPlan = %q, want gpt-5.5", got)
	}
	body, ok, err := p.retryBody("gpt-5.5", false, 400, `{"error":{"message":"gpt-5.5 is not available on free plan"}}`, &Payload{})
	if err != nil || ok || body != nil {
		t.Fatalf("disabled retryBody = (%v, %v, %v), want (nil, false, nil)", body, ok, err)
	}
}

func TestDowngradePolicy_isReactiveFreePlanRejectionMessage(t *testing.T) {
	t.Parallel()
	p := newDowngradePolicy(Config{})
	if !p.isReactiveFreePlanRejectionMessage("gpt-5.5 is not available on free plan") {
		t.Fatal("expected free-plan rejection message match")
	}
	if p.isReactiveFreePlanRejectionMessage("model not found") {
		t.Fatal("unrelated message must not match")
	}
	disabled := newDowngradePolicy(Config{GPT55DowngradeDisabled: true})
	if disabled.isReactiveFreePlanRejectionMessage("gpt-5.5 is not available on free plan") {
		t.Fatal("disabled policy must not match")
	}
}

func TestDowngradePolicy_isFreePlanRejection_customSource(t *testing.T) {
	t.Parallel()
	p := newDowngradePolicy(Config{
		GPT55DowngradeSourceModel: "custom-src",
		GPT55DowngradeTargetModel: "custom-dst",
	})
	body := `{"error":{"message":"custom-src is not available on free plan"}}`
	if !p.isFreePlanRejection(400, body) {
		t.Fatal("expected custom source rejection")
	}
	if p.isFreePlanRejection(400, `{"error":{"message":"gpt-5.5 is not available on free plan"}}`) {
		t.Fatal("default source token must not match custom policy")
	}
}

func TestDowngradePolicy_shouldReactiveRetry(t *testing.T) {
	t.Parallel()
	p := newDowngradePolicy(Config{})
	msg := "gpt-5.5 is not available on free plan"
	if !p.shouldReactiveRetry("gpt-5.5", false, msg) {
		t.Fatal("expected reactive retry")
	}
	if p.shouldReactiveRetry("gpt-5.5", true, msg) {
		t.Fatal("already retried must not retry")
	}
	if p.shouldReactiveRetry("gpt-5.3-codex-spark", false, msg) {
		t.Fatal("non-source model must not retry")
	}
	custom := newDowngradePolicy(Config{GPT55DowngradeSourceModel: "custom-src"})
	if !custom.shouldReactiveRetry("custom-src", false, "custom-src is not available on free plan") {
		t.Fatal("custom source should retry")
	}
}
