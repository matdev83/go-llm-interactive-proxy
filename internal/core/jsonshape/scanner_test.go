package jsonshape_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonshape"
)

func TestScanner_BasicDocuments(t *testing.T) {
	t.Parallel()

	docs := []struct {
		name string
		json string
	}{
		{name: "empty object", json: `{}`},
		{name: "empty array", json: `[]`},
		{name: "simple object", json: `{"a": 1, "b": "hello"}`},
		{name: "nested object", json: `{"a": {"b": {"c": [1, 2, 3]}}}`},
		{name: "mixed array", json: `[1, "two", true, false, null]`},
		{name: "scalar integer", json: `42`},
		{name: "scalar negative float", json: `-3.1415`},
		{name: "scalar string", json: `"hello world"`},
		{name: "scalar true", json: `true`},
		{name: "scalar false", json: `false`},
		{name: "scalar null", json: `null`},
	}

	for _, tc := range docs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			expected, expErr := jsonshape.Preflight([]byte(tc.json), jsonshape.Limits{})
			if expErr != nil {
				t.Fatalf("Preflight failed unexpectedly for valid JSON: %v", expErr)
			}

			s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
			if err := s.Feed([]byte(tc.json)); err != nil {
				t.Fatalf("Feed failed: %v", err)
			}
			res, err := s.Finish()
			if err != nil {
				t.Fatalf("Finish failed: %v", err)
			}
			if res.Bytes != expected.Bytes {
				t.Errorf("Bytes mismatch: got %d, want %d", res.Bytes, expected.Bytes)
			}
			if res.Tokens != expected.Tokens {
				t.Errorf("Tokens mismatch: got %d, want %d", res.Tokens, expected.Tokens)
			}
			if res.MaxDepth != expected.MaxDepth {
				t.Errorf("MaxDepth mismatch: got %d, want %d", res.MaxDepth, expected.MaxDepth)
			}
		})
	}
}

func TestScanner_ChunkedFeeds(t *testing.T) {
	t.Parallel()

	doc := `{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello, world!"}], "stream": true, "max_tokens": 1024}`
	expected, expErr := jsonshape.Preflight([]byte(doc), jsonshape.Limits{})
	if expErr != nil {
		t.Fatalf("Preflight failed: %v", expErr)
	}

	chunkSizes := []int{1, 2, 3, 5, 7, 16, 32, 64, len(doc)}
	for _, sz := range chunkSizes {
		s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
		data := []byte(doc)
		for i := 0; i < len(data); i += sz {
			end := i + sz
			if end > len(data) {
				end = len(data)
			}
			if err := s.Feed(data[i:end]); err != nil {
				t.Fatalf("Feed sz=%d at offset %d: %v", sz, i, err)
			}
		}
		res, err := s.Finish()
		if err != nil {
			t.Fatalf("Finish sz=%d: %v", sz, err)
		}
		if res.Bytes != expected.Bytes || res.Tokens != expected.Tokens || res.MaxDepth != expected.MaxDepth {
			t.Errorf("Result mismatch for sz=%d: got %+v, want %+v", sz, res, expected)
		}
	}
}

func TestScanner_UTF8_Boundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		chunks   [][]byte
		wantKind jsonshape.Kind
	}{
		{
			name:   "2-byte utf-8 split across chunks",
			chunks: [][]byte{[]byte(`{"k": "`), {0xC2}, {0xA9}, []byte(`"}`)},
		},
		{
			name:   "3-byte utf-8 split 1 and 2",
			chunks: [][]byte{[]byte(`{"euro": "`), {0xE2}, {0x82, 0xAC}, []byte(`"}`)},
		},
		{
			name:   "3-byte utf-8 split 2 and 1",
			chunks: [][]byte{[]byte(`{"euro": "`), {0xE2, 0x82}, {0xAC}, []byte(`"}`)},
		},
		{
			name:   "4-byte utf-8 split 1, 1, 2",
			chunks: [][]byte{[]byte(`{"emoji": "`), {0xF0}, {0x9F}, {0x98, 0x80}, []byte(`"}`)},
		},
		{
			name:     "invalid raw byte 0xFF inside string",
			chunks:   [][]byte{[]byte(`{"k": "abc`), {0xFF}, []byte(`def"}`)},
			wantKind: jsonshape.KindInvalidUTF8,
		},
		{
			name:     "invalid raw byte 0xFF in whitespace",
			chunks:   [][]byte{[]byte(`{ `), {0xFF}, []byte(` "k": 1}`)},
			wantKind: jsonshape.KindInvalidUTF8,
		},
		{
			name:     "incomplete utf-8 at EOF",
			chunks:   [][]byte{[]byte(`{"k": "`), {0xC2}},
			wantKind: jsonshape.KindInvalidUTF8,
		},
		{
			name:     "overlong utf-8 0xC0",
			chunks:   [][]byte{[]byte(`{"k": "`), {0xC0, 0x80}, []byte(`"}`)},
			wantKind: jsonshape.KindInvalidUTF8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
			var feedErr error
			for _, c := range tt.chunks {
				if feedErr = s.Feed(c); feedErr != nil {
					break
				}
			}
			var finalErr error
			if feedErr != nil {
				finalErr = feedErr
			} else {
				_, finalErr = s.Finish()
			}

			if tt.wantKind == "" {
				if finalErr != nil {
					t.Fatalf("unexpected error: %v", finalErr)
				}
			} else {
				if got := jsonshape.Classify(finalErr); got != tt.wantKind {
					t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, tt.wantKind, finalErr)
				}
			}
		})
	}
}

func TestScanner_EscapesAndSurrogates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		chunks   [][]byte
		wantKind jsonshape.Kind
	}{
		{
			name:   "simple escapes",
			chunks: [][]byte{[]byte(`{"k": "\" \\ \/ \b \f \n \r \t"}`)},
		},
		{
			name:   "unicode escape split across chunks",
			chunks: [][]byte{[]byte(`{"k": "\u`), []byte(`20`), []byte(`ac"}`)},
		},
		{
			name:   "surrogate pair split across chunks",
			chunks: [][]byte{[]byte(`{"k": "\uD83D`), []byte(`\uDE00"}`)},
		},
		{
			name:   "surrogate pair split between \\ and u",
			chunks: [][]byte{[]byte(`{"k": "\uD83D\`), []byte(`uDE00"}`)},
		},
		{
			name:     "invalid hex in unicode escape",
			chunks:   [][]byte{[]byte(`{"k": "\u123z"}`)},
			wantKind: jsonshape.KindMalformed,
		},
		{
			name:     "invalid escape character \\a",
			chunks:   [][]byte{[]byte(`{"k": "\a"}`)},
			wantKind: jsonshape.KindMalformed,
		},
		{
			name:     "unescaped control char newline in string",
			chunks:   [][]byte{[]byte("{\"k\": \"line1\nline2\"}")},
			wantKind: jsonshape.KindMalformed,
		},
		{
			name:     "unescaped null byte in string",
			chunks:   [][]byte{[]byte("{\"k\": \"null\x00byte\"}")},
			wantKind: jsonshape.KindMalformed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
			var feedErr error
			for _, c := range tt.chunks {
				if feedErr = s.Feed(c); feedErr != nil {
					break
				}
			}
			var finalErr error
			if feedErr != nil {
				finalErr = feedErr
			} else {
				_, finalErr = s.Finish()
			}

			if tt.wantKind == "" {
				if finalErr != nil {
					t.Fatalf("unexpected error: %v", finalErr)
				}
			} else {
				if got := jsonshape.Classify(finalErr); got != tt.wantKind {
					t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, tt.wantKind, finalErr)
				}
			}
		})
	}
}

func TestScanner_NumberGrammar(t *testing.T) {
	t.Parallel()

	validNumbers := []string{
		`0`, `42`, `-42`, `3.14`, `-0.5`, `1e10`, `1E+10`, `1.5e-3`, `0.12345`,
	}
	for _, num := range validNumbers {
		t.Run("valid_"+num, func(t *testing.T) {
			s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
			if err := s.Feed([]byte(num)); err != nil {
				t.Fatalf("Feed failed: %v", err)
			}
			if _, err := s.Finish(); err != nil {
				t.Fatalf("Finish failed: %v", err)
			}
		})
	}

	invalidNumbers := []struct {
		name string
		raw  string
	}{
		{name: "leading zero", raw: `01`},
		{name: "plus sign", raw: `+1`},
		{name: "dangling minus", raw: `-`},
		{name: "dangling dot", raw: `1.`},
		{name: "dangling exponent", raw: `1e`},
		{name: "dangling exponent sign", raw: `1e+`},
		{name: "double dot", raw: `1..2`},
		{name: "hex number", raw: `0x1f`},
	}
	for _, tt := range invalidNumbers {
		t.Run("invalid_"+tt.name, func(t *testing.T) {
			s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
			feedErr := s.Feed([]byte(tt.raw))
			var finalErr error
			if feedErr != nil {
				finalErr = feedErr
			} else {
				_, finalErr = s.Finish()
			}
			if got := jsonshape.Classify(finalErr); got != jsonshape.KindMalformed {
				t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, jsonshape.KindMalformed, finalErr)
			}
		})
	}
}

func TestScanner_Limits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     string
		limits   jsonshape.Limits
		wantKind jsonshape.Kind
	}{
		{
			name:     "bytes over limit",
			data:     `{"x":1}`,
			limits:   jsonshape.Limits{MaxBytes: int64(len(`{"x":1}`) - 1)},
			wantKind: jsonshape.KindTooLarge,
		},
		{
			name:     "depth over limit",
			data:     `[[[1]]]`,
			limits:   jsonshape.Limits{MaxDepth: 2},
			wantKind: jsonshape.KindTooDeep,
		},
		{
			name:     "tokens over limit",
			data:     `[1,2,3]`,
			limits:   jsonshape.Limits{MaxTokens: 4},
			wantKind: jsonshape.KindTooManyTokens,
		},
		{
			name:     "array elements over limit",
			data:     `[1,2,3]`,
			limits:   jsonshape.Limits{MaxArrayElems: 2},
			wantKind: jsonshape.KindTooManyItems,
		},
		{
			name:     "object keys over limit",
			data:     `{"a":1,"b":2}`,
			limits:   jsonshape.Limits{MaxObjectKeys: 1},
			wantKind: jsonshape.KindTooManyItems,
		},
		{
			name:     "string bytes over limit",
			data:     `"abcd"`,
			limits:   jsonshape.Limits{MaxStringBytes: 3},
			wantKind: jsonshape.KindStringTooLong,
		},
		{
			name:     "escaped string bytes over limit",
			data:     `"\u20ac"`,
			limits:   jsonshape.Limits{MaxStringBytes: 2},
			wantKind: jsonshape.KindStringTooLong,
		},
		{
			name:     "key bytes over limit",
			data:     `{"abcd":1}`,
			limits:   jsonshape.Limits{MaxKeyBytes: 3},
			wantKind: jsonshape.KindKeyTooLong,
		},
		{
			name:     "number literal over limit",
			data:     `123456`,
			limits:   jsonshape.Limits{MaxNumberBytes: 5},
			wantKind: jsonshape.KindNumberTooLong,
		},
		{
			name:     "duplicate keys rejected",
			data:     `{"a":1,"a":2}`,
			limits:   jsonshape.Limits{RejectDuplicateNames: true},
			wantKind: jsonshape.KindDuplicateName,
		},
		{
			name:   "duplicate keys accepted by default",
			data:   `{"a":1,"a":2}`,
			limits: jsonshape.Limits{RejectDuplicateNames: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := jsonshape.NewScanner(context.Background(), tt.limits)
			feedErr := s.Feed([]byte(tt.data))
			var finalErr error
			if feedErr != nil {
				finalErr = feedErr
			} else {
				_, finalErr = s.Finish()
			}

			if tt.wantKind == "" {
				if finalErr != nil {
					t.Fatalf("unexpected error: %v", finalErr)
				}
			} else {
				if got := jsonshape.Classify(finalErr); got != tt.wantKind {
					t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, tt.wantKind, finalErr)
				}
			}
		})
	}
}

func TestScanner_MalformedAndReasons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		data       string
		wantReason jsonshape.MalformedReason
	}{
		{name: "empty", data: "", wantReason: jsonshape.MalformedEmpty},
		{name: "whitespace only", data: "   \t\n\r  ", wantReason: jsonshape.MalformedEmpty},
		{name: "non-json character syntax", data: "nope", wantReason: jsonshape.MalformedSyntax},
		{name: "non-json whitespace", data: "\u00a0", wantReason: jsonshape.MalformedSyntax},
		{name: "multiple values objects", data: "{}{}", wantReason: jsonshape.MalformedMultipleValues},
		{name: "multiple values scalars", data: "1 2", wantReason: jsonshape.MalformedMultipleValues},
		{name: "incomplete object", data: `{"a": 1`, wantReason: jsonshape.MalformedIncomplete},
		{name: "incomplete array", data: `[1, 2`, wantReason: jsonshape.MalformedIncomplete},
		{name: "incomplete colon", data: `{"a":`, wantReason: jsonshape.MalformedIncomplete},
		{name: "unexpected closing brace", data: `}`, wantReason: jsonshape.MalformedUnexpectedClosing},
		{name: "unexpected closing bracket", data: `]`, wantReason: jsonshape.MalformedUnexpectedClosing},
		{name: "mismatched delimiter", data: `{"a": [}`, wantReason: jsonshape.MalformedUnexpectedClosing},
		{name: "trailing data", data: `{"a": 1} trailing`, wantReason: jsonshape.MalformedTrailingData},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{})
			feedErr := s.Feed([]byte(tt.data))
			var finalErr error
			if feedErr != nil {
				finalErr = feedErr
			} else {
				_, finalErr = s.Finish()
			}

			var shapeErr *jsonshape.Error
			if !errors.As(finalErr, &shapeErr) || shapeErr.Kind != jsonshape.KindMalformed {
				t.Fatalf("error=%v, want KindMalformed", finalErr)
			}
			if shapeErr.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %q", shapeErr.Reason, tt.wantReason)
			}
		})
	}
}

func TestScanner_Cancellation(t *testing.T) {
	t.Parallel()

	t.Run("canceled before feed", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s := jsonshape.NewScanner(ctx, jsonshape.Limits{})
		err := s.Feed([]byte(`{"a": 1}`))
		if got := jsonshape.Classify(err); got != jsonshape.KindCanceled {
			t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, jsonshape.KindCanceled, err)
		}
	})

	t.Run("canceled between feeds", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		s := jsonshape.NewScanner(ctx, jsonshape.Limits{})
		if err := s.Feed([]byte(`{"a": `)); err != nil {
			t.Fatalf("Feed 1 failed: %v", err)
		}
		cancel()
		err := s.Feed([]byte(`1}`))
		if got := jsonshape.Classify(err); got != jsonshape.KindCanceled {
			t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, jsonshape.KindCanceled, err)
		}
	})

	t.Run("deadline exceeded before finish", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
		defer cancel()
		s := jsonshape.NewScanner(ctx, jsonshape.Limits{})
		_, err := s.Finish()
		if got := jsonshape.Classify(err); got != jsonshape.KindCanceled {
			t.Fatalf("Classify(err) = %q, want %q (err=%v)", got, jsonshape.KindCanceled, err)
		}
	})
}

func TestScanner_GiantStringNoRetention(t *testing.T) {
	t.Parallel()

	// 512 KiB string fed in 4 KiB chunks
	chunkSize := 4096
	totalSize := 512 * 1024
	limits := jsonshape.Limits{
		MaxBytes:       int64(totalSize + 1024),
		MaxStringBytes: totalSize,
	}

	s := jsonshape.NewScanner(context.Background(), limits)
	if err := s.Feed([]byte(`{"data":"`)); err != nil {
		t.Fatalf("Feed header: %v", err)
	}

	chunk := strings.Repeat("a", chunkSize)
	for sent := 0; sent < totalSize; sent += chunkSize {
		if err := s.Feed([]byte(chunk)); err != nil {
			t.Fatalf("Feed chunk at %d: %v", sent, err)
		}
	}

	if err := s.Feed([]byte(`"}`)); err != nil {
		t.Fatalf("Feed footer: %v", err)
	}

	res, err := s.Finish()
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	if res.Tokens != 4 { // {, data, giant_str, }
		t.Errorf("Tokens = %d, want 4", res.Tokens)
	}
}

func TestScanner_Events(t *testing.T) {
	t.Parallel()

	jsonStr := `{"model":"gpt-4","items":[1,true,null]}`
	var events []jsonshape.Event

	handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
		events = append(events, e)
		return nil
	})

	s := jsonshape.NewScanner(context.Background(), jsonshape.Limits{}, jsonshape.WithEventHandler(handler))
	if err := s.Feed([]byte(jsonStr)); err != nil {
		t.Fatalf("Feed failed: %v", err)
	}
	if _, err := s.Finish(); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	expectedTypes := []jsonshape.EventType{
		jsonshape.EventObjectStart,
		jsonshape.EventKey,
		jsonshape.EventString,
		jsonshape.EventKey,
		jsonshape.EventArrayStart,
		jsonshape.EventNumber,
		jsonshape.EventTrue,
		jsonshape.EventNull,
		jsonshape.EventArrayEnd,
		jsonshape.EventObjectEnd,
	}

	if len(events) != len(expectedTypes) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(expectedTypes), events)
	}

	for i, exp := range expectedTypes {
		if events[i].Type != exp {
			t.Errorf("event %d: type = %v, want %v", i, events[i].Type, exp)
		}
	}

	// Verify key was captured
	if events[1].Key != "model" {
		t.Errorf("event 1 Key = %q, want 'model'", events[1].Key)
	}
	if events[3].Key != "items" {
		t.Errorf("event 3 Key = %q, want 'items'", events[3].Key)
	}
}

func TestScanner_DifferentialPreflight(t *testing.T) {
	t.Parallel()

	corpus := []string{
		`null`, `true`, `false`, `0`, `-1.5e+10`, `""`, `"hi"`, `[]`, `{}`,
		`[1,2,3]`, `{"a":1,"b":[true,null]}`, `{"a":{"b":{"c":[]}}}`,
		`{"euro":"\u20ac"}`,
		`{`, `}`, `[`, `]`, `{"a":}`, `{"a":1,}`, `[1,]`, `{}{}`, `1 2`,
		"\"\xff\"", ``, `   `, `{"a":1} trailing`,
	}

	limits := jsonshape.Limits{
		MaxBytes: 1 << 20, MaxDepth: 64, MaxTokens: 1_000_000,
		MaxArrayElems: 100_000, MaxObjectKeys: 100_000,
		MaxStringBytes: 1 << 20, MaxKeyBytes: 1 << 20, MaxNumberBytes: 1 << 20,
	}

	for _, s := range corpus {
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			data := []byte(s)

			// 1. Run Preflight
			expectedRes, preErr := jsonshape.Preflight(data, limits)

			// 2. Run Scanner on whole data
			scWhole := jsonshape.NewScanner(context.Background(), limits)
			feedErr := scWhole.Feed(data)
			var scErr error
			var scRes jsonshape.Result
			if feedErr != nil {
				scErr = feedErr
			} else {
				scRes, scErr = scWhole.Finish()
			}

			if (preErr == nil) != (scErr == nil) {
				t.Fatalf("Preflight error = %v, Scanner error = %v for input %q", preErr, scErr, s)
			}
			if preErr != nil {
				if got, want := jsonshape.Classify(scErr), jsonshape.Classify(preErr); got != want {
					t.Fatalf("Classify mismatch for %q: got %q, want %q (scErr=%v, preErr=%v)", s, got, want, scErr, preErr)
				}
			} else {
				if scRes.Bytes != expectedRes.Bytes || scRes.Tokens != expectedRes.Tokens || scRes.MaxDepth != expectedRes.MaxDepth {
					t.Fatalf("Result mismatch for %q: got %+v, want %+v", s, scRes, expectedRes)
				}
			}

			// 3. Run Scanner 1-byte chunked
			scByte := jsonshape.NewScanner(context.Background(), limits)
			var byteErr error
			var byteRes jsonshape.Result
			for i := 0; i < len(data); i++ {
				if byteErr = scByte.Feed(data[i : i+1]); byteErr != nil {
					break
				}
			}
			if byteErr == nil {
				byteRes, byteErr = scByte.Finish()
			}

			if (preErr == nil) != (byteErr == nil) {
				t.Fatalf("Preflight error = %v, Byte-chunked Scanner error = %v for input %q", preErr, byteErr, s)
			}
			if preErr != nil {
				if got, want := jsonshape.Classify(byteErr), jsonshape.Classify(preErr); got != want {
					t.Fatalf("Byte-chunked Classify mismatch for %q: got %q, want %q", s, got, want)
				}
			} else {
				if byteRes.Bytes != expectedRes.Bytes || byteRes.Tokens != expectedRes.Tokens || byteRes.MaxDepth != expectedRes.MaxDepth {
					t.Fatalf("Byte-chunked Result mismatch for %q: got %+v, want %+v", s, byteRes, expectedRes)
				}
			}
		})
	}
}
