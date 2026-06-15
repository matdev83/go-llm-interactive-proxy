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
	f.Add("{ttft_timeout=60}openai:gpt-4")
	f.Add("{ttft_timeout=60}[ttft_timeout=30]openai:gpt-4^[ttft_timeout=20]gemini:model")
	f.Add("[weight=2,ttft_timeout=30]openai:gpt-4")
	f.Add("a:m1!b:m2!c:m3")
	f.Add("[handicap=10]nvidia:kimi![handicap=5]nvidia:mini!nvidia:flash")
	f.Add("[handicap=10,ttft_timeout=10]a:m![ttft_timeout=5]b:m!c:m")
	f.Add("a:m!b:m|c:m!d:m")
	f.Add("[handicap=3]a:m![handicap=1]b:m!c:m?note=hi")
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4<<10 {
			return
		}
		for _, r := range s {
			if !unicode.IsPrint(r) && r != ' ' {
				return
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
