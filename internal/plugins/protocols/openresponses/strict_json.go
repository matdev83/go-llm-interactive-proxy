package openresponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// validateJSONStrict checks for valid UTF-8, duplicate object keys, maximum JSON depth, and trailing data.
func validateJSONStrict(data []byte, maxDepth int) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("%w: invalid UTF-8 encoding", ErrDecodeFailed)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	type stateKind int
	const (
		stateObject stateKind = iota
		stateArray
	)

	type frame struct {
		kind         stateKind
		seenKeys     map[string]bool
		expectingKey bool
	}

	var stack []frame
	depth := 0
	hasReadRoot := false

	for {
		t, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrDecodeFailed, err)
		}

		if hasReadRoot && len(stack) == 0 {
			return ErrTrailingData
		}

		switch v := t.(type) {
		case json.Delim:
			switch v {
			case '{':
				depth++
				if depth > maxDepth {
					return fmt.Errorf("%w: JSON depth %d exceeds limit %d", ErrDecodeFailed, depth, maxDepth)
				}
				if len(stack) > 0 && stack[len(stack)-1].kind == stateObject {
					top := &stack[len(stack)-1]
					if top.expectingKey {
						return fmt.Errorf("%w: expected object key, got '{'", ErrDecodeFailed)
					}
				}
				stack = append(stack, frame{
					kind:         stateObject,
					seenKeys:     make(map[string]bool),
					expectingKey: true,
				})
			case '}':
				if len(stack) == 0 || stack[len(stack)-1].kind != stateObject {
					return fmt.Errorf("%w: unexpected '}'", ErrDecodeFailed)
				}
				stack = stack[:len(stack)-1]
				depth--
				if len(stack) == 0 {
					hasReadRoot = true
				} else if stack[len(stack)-1].kind == stateObject {
					stack[len(stack)-1].expectingKey = true
				}
			case '[':
				depth++
				if depth > maxDepth {
					return fmt.Errorf("%w: JSON depth %d exceeds limit %d", ErrDecodeFailed, depth, maxDepth)
				}
				if len(stack) > 0 && stack[len(stack)-1].kind == stateObject {
					top := &stack[len(stack)-1]
					if top.expectingKey {
						return fmt.Errorf("%w: expected object key, got '['", ErrDecodeFailed)
					}
				}
				stack = append(stack, frame{
					kind: stateArray,
				})
			case ']':
				if len(stack) == 0 || stack[len(stack)-1].kind != stateArray {
					return fmt.Errorf("%w: unexpected ']'", ErrDecodeFailed)
				}
				stack = stack[:len(stack)-1]
				depth--
				if len(stack) == 0 {
					hasReadRoot = true
				} else if stack[len(stack)-1].kind == stateObject {
					stack[len(stack)-1].expectingKey = true
				}
			}
		case string:
			if len(stack) > 0 && stack[len(stack)-1].kind == stateObject {
				top := &stack[len(stack)-1]
				if top.expectingKey {
					if top.seenKeys[v] {
						return fmt.Errorf("%w: duplicate key %q", ErrDecodeFailed, v)
					}
					top.seenKeys[v] = true
					top.expectingKey = false
				} else {
					top.expectingKey = true
				}
			} else if len(stack) == 0 {
				hasReadRoot = true
			}
		default: // bool, number, null
			if len(stack) > 0 && stack[len(stack)-1].kind == stateObject {
				top := &stack[len(stack)-1]
				if top.expectingKey {
					return fmt.Errorf("%w: expected object key, got value", ErrDecodeFailed)
				}
				top.expectingKey = true
			} else if len(stack) == 0 {
				hasReadRoot = true
			}
		}
	}

	if len(stack) > 0 {
		return fmt.Errorf("%w: unclosed JSON structure", ErrDecodeFailed)
	}

	if dec.More() {
		return ErrTrailingData
	}

	return nil
}
