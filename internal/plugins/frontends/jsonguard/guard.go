// Package jsonguard provides low-cost preflight checks for untrusted frontend JSON bodies.
package jsonguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode"
	"unicode/utf8"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const defaultMaxDepth = 128

// Limits bounds JSON body shape before adapter-specific decode.
type Limits struct {
	MaxBytes       int64
	MaxDepth       int
	MaxTokens      int
	MaxArrayElems  int
	MaxObjectKeys  int
	MaxStringBytes int
	MaxKeyBytes    int
}

// Result reports basic facts gathered during token-level scanning.
type Result struct {
	Bytes    int
	Tokens   int
	MaxDepth int
}

// Kind classifies guard failures for frontend handler mapping.
type Kind string

const (
	KindTooLarge      Kind = "too_large"
	KindMalformed     Kind = "malformed"
	KindTooDeep       Kind = "too_deep"
	KindTooManyTokens Kind = "too_many_tokens"
	KindTooManyItems  Kind = "too_many_items"
	KindStringTooLong Kind = "string_too_long"
	KindKeyTooLong    Kind = "key_too_long"
)

// Error is a typed JSON guard failure.
type Error struct {
	Kind  Kind
	Limit int
	Value int
	Msg   string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Msg != "" {
		return "jsonguard: " + e.Msg
	}
	if e.Limit > 0 {
		return fmt.Sprintf("jsonguard: %s: value %d exceeds limit %d", e.Kind, e.Value, e.Limit)
	}
	return "jsonguard: " + string(e.Kind)
}

// DefaultLimits returns conservative non-zero defaults for frontend JSON bodies.
func DefaultLimits() Limits {
	return Limits{
		MaxBytes:       reqbody.DefaultMaxBytes,
		MaxDepth:       defaultMaxDepth,
		MaxTokens:      1_000_000,
		MaxArrayElems:  100_000,
		MaxObjectKeys:  100_000,
		MaxStringBytes: min(lipapi.MaxPartTextBytes, int(reqbody.DefaultMaxBytes)),
		MaxKeyBytes:    16 << 10,
	}
}

// NormalizeLimits fills zero or negative fields with defaults.
func NormalizeLimits(limits Limits) Limits {
	defaults := DefaultLimits()
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = defaults.MaxDepth
	}
	if limits.MaxTokens <= 0 {
		limits.MaxTokens = defaults.MaxTokens
	}
	if limits.MaxArrayElems <= 0 {
		limits.MaxArrayElems = defaults.MaxArrayElems
	}
	if limits.MaxObjectKeys <= 0 {
		limits.MaxObjectKeys = defaults.MaxObjectKeys
	}
	if limits.MaxStringBytes <= 0 {
		limits.MaxStringBytes = defaults.MaxStringBytes
	}
	if limits.MaxKeyBytes <= 0 {
		limits.MaxKeyBytes = defaults.MaxKeyBytes
	}
	return limits
}

// Preflight validates JSON size and shape using streaming decoder tokens.
func Preflight(data []byte, limits Limits) (Result, error) {
	limits = NormalizeLimits(limits)
	result := Result{Bytes: len(data)}
	if int64(len(data)) > limits.MaxBytes {
		return result, &Error{Kind: KindTooLarge, Limit: int(limits.MaxBytes), Value: len(data)}
	}
	if whitespaceOnly(data) {
		return result, &Error{Kind: KindMalformed, Msg: "empty JSON body"}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	frames := make([]frame, 0, 8)
	rootValues := 0

	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return result, &Error{Kind: KindMalformed, Msg: err.Error()}
		}
		result.Tokens++
		if result.Tokens > limits.MaxTokens {
			return result, &Error{Kind: KindTooManyTokens, Limit: limits.MaxTokens, Value: result.Tokens}
		}

		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{':
				if len(frames) == 0 {
					rootValues++
					if rootValues > 1 {
						return result, &Error{Kind: KindMalformed, Msg: "multiple JSON values"}
					}
				} else if err := countValue(&frames, limits); err != nil {
					return result, err
				}
				frames = append(frames, frame{object: true})
				if err := checkDepth(len(frames), limits.MaxDepth); err != nil {
					return result, err
				}
				result.MaxDepth = max(result.MaxDepth, len(frames))
			case '[':
				if len(frames) == 0 {
					rootValues++
					if rootValues > 1 {
						return result, &Error{Kind: KindMalformed, Msg: "multiple JSON values"}
					}
				} else if err := countValue(&frames, limits); err != nil {
					return result, err
				}
				frames = append(frames, frame{})
				if err := checkDepth(len(frames), limits.MaxDepth); err != nil {
					return result, err
				}
				result.MaxDepth = max(result.MaxDepth, len(frames))
			case '}', ']':
				if len(frames) == 0 {
					return result, &Error{Kind: KindMalformed, Msg: "unexpected closing delimiter"}
				}
				frames = frames[:len(frames)-1]
			}
			continue
		}

		if len(frames) == 0 {
			rootValues++
			if rootValues > 1 {
				return result, &Error{Kind: KindMalformed, Msg: "multiple JSON values"}
			}
		}
		if err := inspectScalar(tok, &frames, limits); err != nil {
			return result, err
		}
	}

	if rootValues == 0 {
		return result, &Error{Kind: KindMalformed, Msg: "empty JSON body"}
	}
	if len(frames) != 0 {
		return result, &Error{Kind: KindMalformed, Msg: "incomplete JSON body"}
	}
	if hasTrailingNonWhitespace(data[dec.InputOffset():]) {
		return result, &Error{Kind: KindMalformed, Msg: "trailing data after JSON value"}
	}
	return result, nil
}

// ReadAndPreflight reads a bounded request body and then applies Preflight.
func ReadAndPreflight(w http.ResponseWriter, r *http.Request, limits Limits) ([]byte, Result, error) {
	limits = NormalizeLimits(limits)
	data, err := reqbody.ReadAll(w, r, limits.MaxBytes)
	if err != nil {
		if reqbody.TooLarge(err) {
			return data, Result{Bytes: len(data)}, &Error{Kind: KindTooLarge, Limit: int(limits.MaxBytes), Value: len(data), Msg: err.Error()}
		}
		return data, Result{Bytes: len(data)}, err
	}
	result, err := Preflight(data, limits)
	return data, result, err
}

// Classify returns the guard kind for typed guard errors.
func Classify(err error) Kind {
	var guardErr *Error
	if errors.As(err, &guardErr) {
		return guardErr.Kind
	}
	return ""
}

// TooLarge reports whether err maps to a request-entity-too-large response.
func TooLarge(err error) bool {
	return Classify(err) == KindTooLarge || reqbody.TooLarge(err)
}

type frame struct {
	object    bool
	count     int
	expectKey bool
}

func checkDepth(depth, limit int) error {
	if depth > limit {
		return &Error{Kind: KindTooDeep, Limit: limit, Value: depth}
	}
	return nil
}

func inspectScalar(tok json.Token, frames *[]frame, limits Limits) error {
	if s, ok := tok.(string); ok && len(*frames) > 0 {
		current := &(*frames)[len(*frames)-1]
		if current.object && !current.expectKey {
			current.count++
			if current.count > limits.MaxObjectKeys {
				return &Error{Kind: KindTooManyItems, Limit: limits.MaxObjectKeys, Value: current.count}
			}
			if len(s) > limits.MaxKeyBytes {
				return &Error{Kind: KindKeyTooLong, Limit: limits.MaxKeyBytes, Value: len(s)}
			}
			current.expectKey = true
			return nil
		}
	}

	if err := countValue(frames, limits); err != nil {
		return err
	}
	if s, ok := tok.(string); ok && len(s) > limits.MaxStringBytes {
		return &Error{Kind: KindStringTooLong, Limit: limits.MaxStringBytes, Value: len(s)}
	}
	return nil
}

func countValue(frames *[]frame, limits Limits) error {
	if len(*frames) == 0 {
		return nil
	}
	current := &(*frames)[len(*frames)-1]
	if current.object {
		if !current.expectKey {
			return &Error{Kind: KindMalformed, Msg: "object value without key"}
		}
		current.expectKey = false
		return nil
	}
	current.count++
	if current.count > limits.MaxArrayElems {
		return &Error{Kind: KindTooManyItems, Limit: limits.MaxArrayElems, Value: current.count}
	}
	return nil
}

func whitespaceOnly(data []byte) bool {
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			return false
		}
		if !unicode.IsSpace(r) {
			return false
		}
		data = data[size:]
	}
	return true
}

func hasTrailingNonWhitespace(data []byte) bool {
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			return true
		}
		if !unicode.IsSpace(r) {
			return true
		}
		data = data[size:]
	}
	return false
}
