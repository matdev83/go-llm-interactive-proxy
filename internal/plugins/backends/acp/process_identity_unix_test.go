//go:build !windows

package acp

import "testing"

func TestParseUint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  uint64
		ok    bool
	}{
		{"42", 42, true},
		{"0", 0, true},
		{"", 0, false},
		{"  123  ", 123, true},
		{"abc", 0, false},
		{"-1", 0, false},
	}
	for _, c := range cases {
		got, ok := parseUint(c.input)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseUint(%q) = (%d, %v), want (%d, %v)", c.input, got, ok, c.want, c.ok)
		}
	}
}
