package toolcallrepair

import (
	"bytes"
	"context"
	"encoding/json"
	"unicode/utf8"
)

type tailRepairKind uint8

const (
	tailRepairNone tailRepairKind = iota
	tailRepairTrailingComma
	tailRepairPendingRootValue
)

type tailAnalysis struct {
	kind            tailRepairKind
	commaOffset     int
	propertyName    string
	propertyPresent bool
	closers         []byte
}

func analyzeJSONTail(ctx context.Context, in []byte) (tailAnalysis, bool) {
	if len(in) == 0 || !utf8.Valid(in) {
		return tailAnalysis{}, false
	}
	stack := make([]tailFrame, 0, 8)
	rootDone := false
	terminalComma := -1
	for i := 0; i < len(in); {
		if err := tailContextErr(ctx); err != nil {
			return tailAnalysis{}, false
		}
		if isJSONWhitespace(in[i]) {
			i++
			continue
		}
		if len(stack) == 0 {
			if rootDone {
				return tailAnalysis{}, false
			}
			next, ok := consumeValue(ctx, in, i)
			if ok {
				if next == i+1 && (in[i] == '{' || in[i] == '[') {
					stack = append(stack, newTailFrame(in[i]))
				} else {
					rootDone = true
				}
				i = next
				terminalComma = -1
				continue
			}
			return tailAnalysis{}, false
		}

		f := &stack[len(stack)-1]
		switch f.state {
		case tailObjectKeyOrEnd:
			if in[i] == '}' {
				i++
				stack = stack[:len(stack)-1]
				if !completeTailChild(&stack, &rootDone) {
					return tailAnalysis{}, false
				}
				terminalComma = -1
				continue
			}
			if in[i] != '"' {
				return tailAnalysis{}, false
			}
			next, key, ok := consumeJSONString(ctx, in, i)
			if !ok {
				return tailAnalysis{}, false
			}
			f.key = key
			f.propertyPresent = true
			f.state = tailObjectColon
			i = next
			terminalComma = -1
		case tailObjectKeyAfterComma:
			if in[i] != '"' {
				return tailAnalysis{}, false
			}
			next, key, ok := consumeJSONString(ctx, in, i)
			if !ok {
				return tailAnalysis{}, false
			}
			f.key = key
			f.propertyPresent = true
			f.state = tailObjectColon
			i = next
			terminalComma = -1
		case tailObjectColon:
			if in[i] != ':' {
				return tailAnalysis{}, false
			}
			f.state = tailObjectValue
			i++
			terminalComma = -1
		case tailObjectValue:
			next, ok := consumeValue(ctx, in, i)
			if !ok {
				return tailAnalysis{}, false
			}
			if next == i+1 && (in[i] == '{' || in[i] == '[') {
				if len(stack) >= maxJSONScanDepth {
					return tailAnalysis{}, false
				}
				stack = append(stack, newTailFrame(in[i]))
			} else {
				f.state = tailObjectCommaOrEnd
			}
			i = next
			terminalComma = -1
		case tailObjectCommaOrEnd:
			if in[i] == ',' {
				f.state = tailObjectKeyAfterComma
				terminalComma = i
				i++
				continue
			}
			if in[i] == '}' {
				i++
				stack = stack[:len(stack)-1]
				if !completeTailChild(&stack, &rootDone) {
					return tailAnalysis{}, false
				}
				terminalComma = -1
				continue
			}
			return tailAnalysis{}, false
		case tailArrayValueOrEnd:
			if in[i] == ']' {
				i++
				stack = stack[:len(stack)-1]
				if !completeTailChild(&stack, &rootDone) {
					return tailAnalysis{}, false
				}
				terminalComma = -1
				continue
			}
			next, ok := consumeValue(ctx, in, i)
			if !ok {
				return tailAnalysis{}, false
			}
			if next == i+1 && (in[i] == '{' || in[i] == '[') {
				if len(stack) >= maxJSONScanDepth {
					return tailAnalysis{}, false
				}
				stack = append(stack, newTailFrame(in[i]))
			} else {
				f.state = tailArrayCommaOrEnd
			}
			i = next
			terminalComma = -1
		case tailArrayValueAfterComma:
			next, ok := consumeValue(ctx, in, i)
			if !ok {
				return tailAnalysis{}, false
			}
			if next == i+1 && (in[i] == '{' || in[i] == '[') {
				if len(stack) >= maxJSONScanDepth {
					return tailAnalysis{}, false
				}
				stack = append(stack, newTailFrame(in[i]))
			} else {
				f.state = tailArrayCommaOrEnd
			}
			i = next
			terminalComma = -1
		case tailArrayCommaOrEnd:
			if in[i] == ',' {
				f.state = tailArrayValueAfterComma
				terminalComma = i
				i++
				continue
			}
			if in[i] == ']' {
				i++
				stack = stack[:len(stack)-1]
				if !completeTailChild(&stack, &rootDone) {
					return tailAnalysis{}, false
				}
				terminalComma = -1
				continue
			}
			return tailAnalysis{}, false
		default:
			return tailAnalysis{}, false
		}
	}

	if err := tailContextErr(ctx); err != nil {
		return tailAnalysis{}, false
	}
	if terminalComma >= 0 && len(stack) > 0 {
		top := stack[len(stack)-1].state
		if top == tailObjectKeyAfterComma || top == tailArrayValueAfterComma {
			return tailAnalysis{kind: tailRepairTrailingComma, commaOffset: terminalComma, closers: tailClosers(stack)}, true
		}
	}
	if !rootDone && len(stack) == 1 {
		f := stack[0]
		if f.kind == '{' && f.state == tailObjectValue && f.propertyPresent {
			return tailAnalysis{kind: tailRepairPendingRootValue, propertyName: f.key, propertyPresent: true, closers: tailClosers(stack)}, true
		}
	}
	return tailAnalysis{}, false
}

type tailState uint8

const (
	tailObjectKeyOrEnd tailState = iota
	tailObjectKeyAfterComma
	tailObjectColon
	tailObjectValue
	tailObjectCommaOrEnd
	tailArrayValueOrEnd
	tailArrayValueAfterComma
	tailArrayCommaOrEnd
)

type tailFrame struct {
	kind            byte
	state           tailState
	key             string
	propertyPresent bool
}

func newTailFrame(kind byte) tailFrame {
	if kind == '{' {
		return tailFrame{kind: kind, state: tailObjectKeyOrEnd}
	}
	return tailFrame{kind: kind, state: tailArrayValueOrEnd}
}

func completeTailChild(stack *[]tailFrame, rootDone *bool) bool {
	if len(*stack) == 0 {
		*rootDone = true
		return true
	}
	parent := &(*stack)[len(*stack)-1]
	switch parent.state {
	case tailObjectValue:
		parent.state = tailObjectCommaOrEnd
	case tailArrayValueOrEnd, tailArrayValueAfterComma:
		parent.state = tailArrayCommaOrEnd
	default:
		return false
	}
	return true
}

func tailClosers(stack []tailFrame) []byte {
	out := make([]byte, 0, len(stack))
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].kind == '{' {
			out = append(out, '}')
		} else {
			out = append(out, ']')
		}
	}
	return out
}

func isJSONWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

func consumeValue(ctx context.Context, in []byte, i int) (int, bool) {
	if i >= len(in) {
		return i, false
	}
	switch in[i] {
	case '{', '[':
		return i + 1, true
	case '"':
		next, _, ok := consumeJSONString(ctx, in, i)
		return next, ok
	default:
		return consumePrimitive(ctx, in, i)
	}
}

func consumeJSONString(ctx context.Context, in []byte, i int) (int, string, bool) {
	if i >= len(in) || in[i] != '"' {
		return i, "", false
	}
	for j := i + 1; j < len(in); j++ {
		if j&255 == 0 {
			if err := tailContextErr(ctx); err != nil {
				return j, "", false
			}
		}
		c := in[j]
		if c < 0x20 {
			return j, "", false
		}
		if c == '\\' {
			j++
			if j >= len(in) {
				return j, "", false
			}
			switch in[j] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if j+4 >= len(in) {
					return j, "", false
				}
				for k := j + 1; k <= j+4; k++ {
					if !isHex(in[k]) {
						return k, "", false
					}
				}
				j += 4
			default:
				return j, "", false
			}
			continue
		}
		if c == '"' {
			var value string
			if err := json.Unmarshal(in[i:j+1], &value); err != nil {
				return j, "", false
			}
			return j + 1, value, true
		}
	}
	return len(in), "", false
}

func consumePrimitive(ctx context.Context, in []byte, i int) (int, bool) {
	start := i
	for i < len(in) && !isPrimitiveDelimiter(in[i]) {
		if i&255 == 0 {
			if err := tailContextErr(ctx); err != nil {
				return i, false
			}
		}
		i++
	}
	if start == i {
		return i, false
	}
	token := in[start:i]
	if bytes.Equal(token, []byte("true")) || bytes.Equal(token, []byte("false")) || bytes.Equal(token, []byte("null")) {
		return i, true
	}
	if json.Valid(token) {
		var v any
		if err := json.Unmarshal(token, &v); err == nil {
			if _, ok := v.(map[string]any); !ok {
				if _, ok := v.([]any); !ok {
					return i, true
				}
			}
		}
	}
	return i, false
}

func tailContextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func isPrimitiveDelimiter(c byte) bool {
	return isJSONWhitespace(c) || c == ',' || c == ']' || c == '}'
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func buildTrailingCommaCandidate(in []byte, a tailAnalysis, maxBytes int) ([]byte, bool) {
	if a.kind != tailRepairTrailingComma || a.commaOffset < 0 || a.commaOffset >= len(in) || in[a.commaOffset] != ',' {
		return nil, false
	}
	outLen := len(in) - 1 + len(a.closers)
	if maxBytes > 0 && outLen > maxBytes {
		return nil, false
	}
	out := make([]byte, 0, outLen)
	out = append(out, in[:a.commaOffset]...)
	out = append(out, in[a.commaOffset+1:]...)
	out = append(out, a.closers...)
	if !json.Valid(out) || !utf8.Valid(out) {
		return nil, false
	}
	return out, true
}

func buildPendingValueCandidate(in []byte, a tailAnalysis, value []byte, maxBytes int) ([]byte, bool) {
	if a.kind != tailRepairPendingRootValue || !a.propertyPresent {
		return nil, false
	}
	outLen := len(in) + len(value) + len(a.closers)
	if maxBytes > 0 && outLen > maxBytes {
		return nil, false
	}
	out := make([]byte, 0, outLen)
	out = append(out, in...)
	out = append(out, value...)
	out = append(out, a.closers...)
	if !json.Valid(out) || !utf8.Valid(out) {
		return nil, false
	}
	return out, true
}
