package routing

import (
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
	if !sel.Alternatives[0].Weighted.Branches[0].First {
		t.Fatalf("expected First")
	}
}
