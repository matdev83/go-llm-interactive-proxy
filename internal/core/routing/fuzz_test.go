package routing

import (
	"testing"
	"unicode"
)

func FuzzParseSelector(f *testing.F) {
	f.Add("openai:gpt-4")
	f.Add("a|b|c")
	f.Add("[weight=2]x:y^[first]p:q")
	f.Add("m?max_tokens=10")
	f.Fuzz(func(t *testing.T, s string) {
		for _, r := range s {
			if !unicode.IsPrint(r) && r != ' ' {
				t.Skip()
			}
		}
		sel, err := Parse(s)
		if err != nil {
			return
		}
		if sel == nil {
			t.Fatal("nil without error")
		}
		_, _ = ExpandFailover(sel, PlanOptions{Rand: rng(1), Session: &SessionRoutingState{FirstRequestConsumed: true}})
	})
}
