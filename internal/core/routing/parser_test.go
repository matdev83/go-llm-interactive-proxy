package routing

import (
	"errors"
	"net/url"
	"testing"
)

func TestParsePrimaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		backend  string
		model    string
		wantErr  bool
		queryKey string
		queryVal string
	}{
		{"gpt-4o", "", "gpt-4o", false, "", ""},
		{"openai:gpt-4o", "openai", "gpt-4o", false, "", ""},
		{"openai.azure:gpt-4o", "openai.azure", "gpt-4o", false, "", ""},
		{"openai:gpt-4o?temperature=0.2", "openai", "gpt-4o", false, "temperature", "0.2"},
		{"", "", "", true, "", ""},
		{"openai:", "", "", true, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			sel, err := Parse(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(sel.Alternatives) != 1 || sel.Alternatives[0].Primary == nil {
				t.Fatalf("expected single primary, got %#v", sel)
			}
			p := sel.Alternatives[0].Primary
			if p.Backend != tc.backend || p.Model != tc.model {
				t.Fatalf("backend/model: got %q %q want %q %q", p.Backend, p.Model, tc.backend, tc.model)
			}
			if tc.queryKey != "" {
				if p.Params.Get(tc.queryKey) != tc.queryVal {
					t.Fatalf("query: %v", p.Params)
				}
			}
		})
	}
}

func TestParseFailoverOrder(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a|b:c|anthropic:opus")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Alternatives) != 3 {
		t.Fatalf("got %d alts", len(sel.Alternatives))
	}
	if sel.Alternatives[0].Primary.Model != "a" {
		t.Fatalf("alt0 model")
	}
	if sel.Alternatives[1].Primary.Backend != "b" || sel.Alternatives[1].Primary.Model != "c" {
		t.Fatalf("alt1")
	}
}

func TestParseWeightedAndFirst(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[weight=3]openai:gpt-4^[weight=1]anthropic:opus")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Alternatives) != 1 || sel.Alternatives[0].Weighted == nil {
		t.Fatalf("expected weighted")
	}
	w := sel.Alternatives[0].Weighted
	if len(w.Branches) != 2 || w.Branches[0].Weight != 3 || w.Branches[1].Weight != 1 {
		t.Fatalf("branches: %#v", w.Branches)
	}
}

func TestParseInvalidTwoFirst(t *testing.T) {
	t.Parallel()
	_, err := Parse("[first]a:b^[first]c:d")
	if err == nil {
		t.Fatal("expected error for two [first]")
	}
}

func TestParseFirstSingleArm(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]cheap:fast")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Alternatives[0].Weighted == nil || len(sel.Alternatives[0].Weighted.Branches) != 1 {
		t.Fatalf("expected one weighted branch")
	}
	if !sel.Alternatives[0].Weighted.Branches[0].IsFirst {
		t.Fatalf("expected IsFirst")
	}
}

// Task 14.5: parity with composite routing examples (failover |, weighted ^, [first], [weight=], per-leg query).
func TestParseParity_pythonLIPCompositeSelector(t *testing.T) {
	t.Parallel()
	s := "[first]openai-codex:gpt-5.3-codex?reasoning_effort=high^[weight=4]openai-codex:gpt-5.3-codex?reasoning_effort=low|[weight=2]openai-codex:gpt-5.3-codex?reasoning_effort=medium"
	sel, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Alternatives) != 2 {
		t.Fatalf("want 2 failover arms, got %d", len(sel.Alternatives))
	}
	// Arm 1: weighted first + second branch with weight 4
	w0 := sel.Alternatives[0].Weighted
	if w0 == nil || len(w0.Branches) != 2 {
		t.Fatalf("arm1 weighted branches: %#v", w0)
	}
	b0 := w0.Branches[0]
	if !b0.IsFirst || b0.Weight != 1 {
		t.Fatalf("branch0: IsFirst=%v Weight=%d", b0.IsFirst, b0.Weight)
	}
	if b0.Target.Backend != "openai-codex" || b0.Target.Model != "gpt-5.3-codex" {
		t.Fatalf("branch0 target: %#v", b0.Target)
	}
	if b0.Target.Params.Get("reasoning_effort") != "high" {
		t.Fatalf("branch0 params: %v", b0.Target.Params)
	}
	b1 := w0.Branches[1]
	if b1.IsFirst || b1.Weight != 4 {
		t.Fatalf("branch1: IsFirst=%v Weight=%d", b1.IsFirst, b1.Weight)
	}
	if b1.Target.Params.Get("reasoning_effort") != "low" {
		t.Fatalf("branch1 params: %v", b1.Target.Params)
	}
	// Arm 2: single weighted branch [weight=2]...
	w1 := sel.Alternatives[1].Weighted
	if w1 == nil || len(w1.Branches) != 1 {
		t.Fatalf("arm2 weighted: %#v", w1)
	}
	if w1.Branches[0].Weight != 2 || w1.Branches[0].IsFirst {
		t.Fatalf("arm2 branch0: %#v", w1.Branches[0])
	}
	if w1.Branches[0].Target.Params.Get("reasoning_effort") != "medium" {
		t.Fatalf("arm2 params: %v", w1.Branches[0].Target.Params)
	}
}

func TestParseInvalidQueryWrapsParseQueryError(t *testing.T) {
	t.Parallel()
	_, err := Parse("openai:gpt-4?x=%zz")
	if err == nil {
		t.Fatal("expected error for invalid query escape")
	}
	if !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("expected ErrInvalidSelector in chain: %v", err)
	}
	var escErr url.EscapeError
	if !errors.As(err, &escErr) {
		t.Fatalf("expected url.EscapeError in chain, got %T: %v", err, err)
	}
}
