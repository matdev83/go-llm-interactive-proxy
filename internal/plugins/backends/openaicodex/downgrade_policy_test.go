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
