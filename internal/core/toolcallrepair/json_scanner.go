package toolcallrepair

import (
	"encoding/json"
	"unicode/utf8"
)

const maxJSONScanDepth = 256

// CompleteJSONSuffix appends the minimal suffix that closes an open string and
// any unclosed object/array delimiters. It never mutates or deletes prefix bytes.
func CompleteJSONSuffix(in []byte) ([]byte, bool) {
	if len(in) == 0 {
		return nil, false
	}
	if json.Valid(in) {
		if !utf8.Valid(in) {
			return nil, false
		}
		out := append([]byte(nil), in...)
		return out, true
	}
	stack := make([]byte, 0, 8)
	inString := false
	escape := false
	for i := range len(in) {
		c := in[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			if len(stack) >= maxJSONScanDepth {
				return nil, false
			}
			stack = append(stack, c)
		case '}':
			if len(stack) == 0 || stack[len(stack)-1] != '{' {
				return nil, false
			}
			stack = stack[:len(stack)-1]
		case ']':
			if len(stack) == 0 || stack[len(stack)-1] != '[' {
				return nil, false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if escape {
		return nil, false
	}
	suffix := make([]byte, 0, 1+len(stack))
	if inString {
		suffix = append(suffix, '"')
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			suffix = append(suffix, '}')
		} else {
			suffix = append(suffix, ']')
		}
	}
	out := make([]byte, len(in)+len(suffix))
	copy(out, in)
	copy(out[len(in):], suffix)
	if !json.Valid(out) || !utf8.Valid(out) {
		return nil, false
	}
	return out, true
}
