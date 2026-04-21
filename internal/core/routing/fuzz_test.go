package routing

import (
	"testing"
	"unicode"
)

func capFuzzBytes(b []byte, max int) []byte {
	if max <= 0 || len(b) <= max {
		return b
	}
	return b[:max]
}

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

// FuzzParseSelectorFromBytes exercises Parse with arbitrary UTF-8 (no printable-only filter).
func FuzzParseSelectorFromBytes(f *testing.F) {
	f.Add([]byte("openai:gpt-4"))
	f.Add([]byte{0xff, 0xfe, 0xfd})
	f.Fuzz(func(t *testing.T, b []byte) {
		b = capFuzzBytes(b, 64<<10)
		s := string(b)
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
