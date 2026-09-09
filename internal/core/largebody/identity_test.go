package largebody_test

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func freezeBaseCall() *lipapi.Call {
	return &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("freeze hello")},
		}},
	}
}

func freezeIntPtr(v int) *int           { return &v }
func freezeFloatPtr(v float64) *float64 { return &v }

func freezeModelExt(model string) map[string]json.RawMessage {
	return map[string]json.RawMessage{"openai.model": json.RawMessage(`"` + model + `"`)}
}

// TestIdentityWriter_StreamingStringEscapeParity tests that streaming string escaping
// produces byte-for-byte identical output to json.Marshal across all character ranges,
// HTML-sensitive characters, control chars, and split UTF-8 runes across chunk boundaries.
func TestIdentityWriter_StreamingStringEscapeParity(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		in   string
	}{
		{"simple ascii", "hello world 123"},
		{"quotes and slashes", `"hello \ "world" \\`},
		{"control chars", "\x00\x01\x02\x1f\n\r\t\b\f"},
		{"html sensitive", "<div>&\"'\\</div>"},
		{"unicode line separators", "\u2028 and \u2029"},
		{"emoji and accents", "🧪 café \\u0041 \n\t\r 🚀🌍"},
		{"tricky full fixture", "<div>&\"'\\</div>\u2028\u2029🧪 café \\u0041 \n\t\r"},
		{"huge string", strings.Repeat("abc <div>&\"'</div> \u2028\u2029 ", 1000)},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			wantBytes, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("json.Marshal failed: %v", err)
			}
			// wantBytes has leading and trailing quotes: `"..."`
			wantEscaped := string(wantBytes[1 : len(wantBytes)-1])

			// Test streaming all at once
			var buf bytes.Buffer
			sw := largebody.NewStreamingEscapeWriter(&buf)
			if _, err := io.WriteString(sw, tc.in); err != nil {
				t.Fatalf("WriteString: %v", err)
			}
			if err := sw.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if got := buf.String(); got != wantEscaped {
				t.Fatalf("all-at-once mismatch:\ngot:  %q\nwant: %q", got, wantEscaped)
			}

			// Test chunked byte-by-byte (tests split UTF-8 rune handling)
			buf.Reset()
			sw = largebody.NewStreamingEscapeWriter(&buf)
			for i := 0; i < len(tc.in); i++ {
				if _, err := sw.Write([]byte{tc.in[i]}); err != nil {
					t.Fatalf("Write single byte %d: %v", i, err)
				}
			}
			if err := sw.Close(); err != nil {
				t.Fatalf("Close byte-by-byte: %v", err)
			}
			if got := buf.String(); got != wantEscaped {
				t.Fatalf("byte-by-byte mismatch:\ngot:  %q\nwant: %q", got, wantEscaped)
			}

			// Test small chunk sizes: 2, 3, 5, 7 bytes
			for _, chunkSize := range []int{2, 3, 5, 7} {
				buf.Reset()
				sw = largebody.NewStreamingEscapeWriter(&buf)
				inBytes := []byte(tc.in)
				for offset := 0; offset < len(inBytes); offset += chunkSize {
					end := offset + chunkSize
					if end > len(inBytes) {
						end = len(inBytes)
					}
					if _, err := sw.Write(inBytes[offset:end]); err != nil {
						t.Fatalf("Write chunk [%d:%d]: %v", offset, end, err)
					}
				}
				if err := sw.Close(); err != nil {
					t.Fatalf("Close chunk size %d: %v", chunkSize, err)
				}
				if got := buf.String(); got != wantEscaped {
					t.Fatalf("chunk size %d mismatch:\ngot:  %q\nwant: %q", chunkSize, got, wantEscaped)
				}
			}
		})
	}
}

// TestIdentityWriter_ParityWithDiagFixtures tests that largebody.CanonicalCallIdentity
// matches diag.StableCallSum byte-for-byte across all frozen canonical fixtures.
func TestIdentityWriter_ParityWithDiagFixtures(t *testing.T) {
	t.Parallel()

	assertParity := func(t *testing.T, name string, call *lipapi.Call) {
		t.Helper()
		wantSum := diag.StableCallSum(call)
		digest := largebody.CanonicalCallIdentity(call)
		if digest.Sum() != wantSum {
			t.Fatalf("[%s] digest mismatch:\ngot:  %x\nwant: %x", name, digest.Sum(), wantSum)
		}

		wantToken := diag.StableCallToken(call)
		if gotToken := digest.Token(); gotToken != wantToken {
			t.Fatalf("[%s] token mismatch: got %q, want %q", name, gotToken, wantToken)
		}

		var explicitID string
		if call != nil {
			explicitID = call.ID
		}
		wantCallID := diag.StableCallID(call)
		if gotCallID := digest.CallID(explicitID); gotCallID != wantCallID {
			t.Fatalf("[%s] callID mismatch: got %q, want %q", name, gotCallID, wantCallID)
		}
	}

	t.Run("nil call", func(t *testing.T) {
		assertParity(t, "nil call", nil)
	})

	t.Run("empty call", func(t *testing.T) {
		assertParity(t, "empty call", &lipapi.Call{})
	})

	t.Run("base call", func(t *testing.T) {
		assertParity(t, "base call", freezeBaseCall())
	})

	t.Run("call with explicit ID", func(t *testing.T) {
		c := freezeBaseCall()
		c.ID = "call-explicit-1"
		assertParity(t, "call with explicit ID", c)
	})

	t.Run("call with whitespace ID", func(t *testing.T) {
		c := freezeBaseCall()
		c.ID = "  call-whitespace-2  "
		assertParity(t, "call with whitespace ID", c)
	})

	t.Run("call with blank ID", func(t *testing.T) {
		c := freezeBaseCall()
		c.ID = "   "
		assertParity(t, "call with blank ID", c)
	})

	t.Run("huge string fixture", func(t *testing.T) {
		c := freezeBaseCall()
		c.Messages[0].Parts[0] = lipapi.TextPart(strings.Repeat("a", 1<<20))
		assertParity(t, "huge string fixture", c)
	})

	t.Run("tricky unicode and html escapes", func(t *testing.T) {
		c := freezeBaseCall()
		c.Messages[0].Parts[0] = lipapi.TextPart("<div>&\"'\\</div>\u2028\u2029🧪 café \\u0041 \n\t\r")
		assertParity(t, "tricky unicode and html escapes", c)
	})

	t.Run("tools and tool choice", func(t *testing.T) {
		c := freezeBaseCall()
		c.Tools = []lipapi.ToolDef{{Name: "get_weather", Description: "lookup"}}
		c.ToolChoice = lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}
		assertParity(t, "tools and tool choice", c)
	})

	t.Run("items shape", func(t *testing.T) {
		c := &lipapi.Call{
			Route: lipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
			Items: []lipapi.Item{{
				Kind:    lipapi.ItemKindMessage,
				ID:      "m1",
				Status:  lipapi.ItemStatusCompleted,
				Role:    lipapi.RoleUser,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "freeze hello"}},
			}},
		}
		assertParity(t, "items shape", c)
	})

	t.Run("model and route extensions", func(t *testing.T) {
		c := freezeBaseCall()
		c.Route.Selector = "stub:gpt-4o"
		c.Extensions = freezeModelExt("gpt-4o-mini")
		assertParity(t, "model and route extensions", c)
	})

	t.Run("session fields", func(t *testing.T) {
		c := freezeBaseCall()
		c.Session.AuthoritativeSessionID = "sess-1"
		c.Session.ClientSessionID = "client-1"
		c.Session.ALegID = "a-1"
		c.Session.ResumeToken = "tok-secret"
		c.Session.ContinuityKey = "ck-1"
		assertParity(t, "session fields", c)
	})

	t.Run("optional generation options", func(t *testing.T) {
		c := freezeBaseCall()
		c.Options = lipapi.GenerationOptions{
			MaxOutputTokens: freezeIntPtr(1024),
			Temperature:     freezeFloatPtr(0.7),
			TopP:            freezeFloatPtr(0.9),
			ReasoningEffort: "high",
			Verbosity:       lipapi.VerbosityLow,
		}
		c.PreviousResponseID = "resp_prev"
		c.PromptCacheKey = "cache_key_1"
		assertParity(t, "optional generation options", c)
	})
}

// TestIdentityWriter_StreamingMessages tests constructing the identity hash
// by streaming large message text parts in chunks without retaining the full string.
func TestIdentityWriter_StreamingMessages(t *testing.T) {
	t.Parallel()

	hugeText := strings.Repeat("Lorem ipsum dolor sit amet <div>&\"'</div> \u2028\u2029 🧪 ", 5000)

	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "stub:gpt-4o"},
		Extensions: freezeModelExt("gpt-4o"),
		Session: lipapi.SessionRef{
			ClientSessionID:        "client-sess-1",
			AuthoritativeSessionID: "auth-sess-1",
			ALegID:                 "aleg-1",
			ResumeToken:            "secret-token",
		},
		Options: lipapi.GenerationOptions{
			MaxOutputTokens: freezeIntPtr(2048),
			Temperature:     freezeFloatPtr(0.7),
		},
		Messages: []lipapi.Message{
			{
				Role:  lipapi.RoleSystem,
				Parts: []lipapi.Part{lipapi.TextPart("You are a helpful assistant.")},
			},
			{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart(hugeText)},
			},
		},
	}

	wantSum := diag.StableCallSum(call)

	// Construct via CallIdentityWriter with streaming text chunks
	cfg := largebody.CallIdentityConfig{
		Session:    call.Session,
		Route:      call.Route,
		Extensions: call.Extensions,
		Options:    call.Options,
	}

	w, err := largebody.NewCallIdentityWriter(cfg)
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}

	// Add system message
	if err := w.AddMessage(call.Messages[0]); err != nil {
		t.Fatalf("AddMessage(system): %v", err)
	}

	// Stream user message with huge text
	msgWriter, err := w.BeginMessage(lipapi.RoleUser)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}

	textWriter, err := msgWriter.BeginTextPart()
	if err != nil {
		t.Fatalf("BeginTextPart: %v", err)
	}

	// Write in 1024-byte chunks
	chunkSize := 1024
	inBytes := []byte(hugeText)
	for i := 0; i < len(inBytes); i += chunkSize {
		end := i + chunkSize
		if end > len(inBytes) {
			end = len(inBytes)
		}
		if _, err := textWriter.Write(inBytes[i:end]); err != nil {
			t.Fatalf("Write chunk: %v", err)
		}
	}
	if err := textWriter.Close(); err != nil {
		t.Fatalf("textWriter.Close: %v", err)
	}
	if err := msgWriter.EndMessage(); err != nil {
		t.Fatalf("msgWriter.EndMessage: %v", err)
	}

	digest, err := w.Digest()
	if err != nil {
		t.Fatalf("w.Digest: %v", err)
	}

	if digest.Sum() != wantSum {
		t.Fatalf("streaming messages sum mismatch:\ngot:  %x\nwant: %x", digest.Sum(), wantSum)
	}
}

// TestIdentityWriter_StreamingItems tests constructing the identity hash
// by streaming large item content parts in chunks without retaining the full string.
func TestIdentityWriter_StreamingItems(t *testing.T) {
	t.Parallel()

	hugeText := strings.Repeat("item text payload <div>&\"'</div> \u2028\u2029 🧪 ", 5000)

	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Items: []lipapi.Item{{
			Kind:    lipapi.ItemKindMessage,
			ID:      "m1",
			Status:  lipapi.ItemStatusCompleted,
			Role:    lipapi.RoleUser,
			Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: hugeText}},
		}},
	}

	wantSum := diag.StableCallSum(call)

	cfg := largebody.CallIdentityConfig{
		Route: call.Route,
	}

	w, err := largebody.NewCallIdentityWriter(cfg)
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}

	itemWriter, err := w.BeginMessageItem("m1", lipapi.ItemStatusCompleted, lipapi.RoleUser, "")
	if err != nil {
		t.Fatalf("BeginMessageItem: %v", err)
	}

	textWriter, err := itemWriter.BeginTextContentPart()
	if err != nil {
		t.Fatalf("BeginTextContentPart: %v", err)
	}

	chunkSize := 512
	inBytes := []byte(hugeText)
	for i := 0; i < len(inBytes); i += chunkSize {
		end := i + chunkSize
		if end > len(inBytes) {
			end = len(inBytes)
		}
		if _, err := textWriter.Write(inBytes[i:end]); err != nil {
			t.Fatalf("Write chunk: %v", err)
		}
	}
	if err := textWriter.Close(); err != nil {
		t.Fatalf("textWriter.Close: %v", err)
	}
	if err := itemWriter.EndItem(); err != nil {
		t.Fatalf("itemWriter.EndItem: %v", err)
	}

	digest, err := w.Digest()
	if err != nil {
		t.Fatalf("w.Digest: %v", err)
	}

	if digest.Sum() != wantSum {
		t.Fatalf("streaming items sum mismatch:\ngot:  %x\nwant: %x", digest.Sum(), wantSum)
	}
}

func TestIdentityWriter_BeginTextContentPart_EmptyText(t *testing.T) {
	t.Parallel()

	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Items: []lipapi.Item{{
			Kind:    lipapi.ItemKindMessage,
			ID:      "m1",
			Status:  lipapi.ItemStatusCompleted,
			Role:    lipapi.RoleUser,
			Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: ""}},
		}},
	}

	wantSum := diag.StableCallSum(call)

	w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
		Route: call.Route,
	})
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}

	itemWriter, err := w.BeginMessageItem("m1", lipapi.ItemStatusCompleted, lipapi.RoleUser, "")
	if err != nil {
		t.Fatalf("BeginMessageItem: %v", err)
	}

	textWriter, err := itemWriter.BeginTextContentPart()
	if err != nil {
		t.Fatalf("BeginTextContentPart: %v", err)
	}
	if err := textWriter.Close(); err != nil {
		t.Fatalf("textWriter.Close: %v", err)
	}
	if err := itemWriter.EndItem(); err != nil {
		t.Fatalf("itemWriter.EndItem: %v", err)
	}

	digest, err := w.Digest()
	if err != nil {
		t.Fatalf("w.Digest: %v", err)
	}

	if digest.Sum() != wantSum {
		t.Fatalf("streaming items empty text sum mismatch:\ngot:  %x\nwant: %x", digest.Sum(), wantSum)
	}
}

// TestIdentityWriter_WriteCall_AllFixtures tests that CallIdentityWriter.WriteCall
// matches diag.StableCallSum on all fixtures.
func TestIdentityWriter_WriteCall_AllFixtures(t *testing.T) {
	t.Parallel()

	fixtures := []*lipapi.Call{
		nil,
		{},
		freezeBaseCall(),
		func() *lipapi.Call {
			c := freezeBaseCall()
			c.ID = "call-custom-123"
			return c
		}(),
		func() *lipapi.Call {
			c := freezeBaseCall()
			c.ID = "   "
			return c
		}(),
		func() *lipapi.Call {
			c := freezeBaseCall()
			c.Messages[0].Parts[0] = lipapi.TextPart("<div>&\"'\\</div>\u2028\u2029🧪 café \\u0041 \n\t\r")
			return c
		}(),
		func() *lipapi.Call {
			c := freezeBaseCall()
			c.Tools = []lipapi.ToolDef{{Name: "get_weather"}}
			c.ToolChoice = lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}
			return c
		}(),
		func() *lipapi.Call {
			return &lipapi.Call{
				Route: lipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
				Items: []lipapi.Item{{
					Kind:    lipapi.ItemKindMessage,
					ID:      "m1",
					Status:  lipapi.ItemStatusCompleted,
					Role:    lipapi.RoleUser,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "freeze hello"}},
				}},
			}
		}(),
		func() *lipapi.Call {
			c := freezeBaseCall()
			c.Options = lipapi.GenerationOptions{
				MaxOutputTokens: freezeIntPtr(512),
				Temperature:     freezeFloatPtr(0.5),
				ReasoningEffort: "low",
			}
			return c
		}(),
	}

	for i, call := range fixtures {
		wantSum := diag.StableCallSum(call)
		w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{})
		if err != nil {
			t.Fatalf("fixture %d NewCallIdentityWriter: %v", i, err)
		}
		digest, err := w.WriteCall(call)
		if err != nil {
			t.Fatalf("fixture %d WriteCall: %v", i, err)
		}
		if digest.Sum() != wantSum {
			t.Fatalf("fixture %d sum mismatch:\ngot:  %x\nwant: %x", i, digest.Sum(), wantSum)
		}
	}
}

// TestIdentityWriter_ConvenienceFields tests that RouteSelector, ClientModel, and
// SessionInput correctly populate their respective Call fields.
func TestIdentityWriter_ConvenienceFields(t *testing.T) {
	t.Parallel()

	cfg := largebody.CallIdentityConfig{
		RouteSelector: "stub:gpt-4o",
		ClientModel:   "gpt-4o",
		SessionInput: largebody.SessionInput{
			AuthoritativeSessionID: "auth-1",
			ClientSessionID:        "client-1",
			ALegID:                 "aleg-1",
			ResumeToken:            largebody.NewSensitiveString("secret-tok"),
		},
	}

	w, err := largebody.NewCallIdentityWriter(cfg)
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}
	digest, err := w.Digest()
	if err != nil {
		t.Fatalf("w.Digest: %v", err)
	}

	expectedCall := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub:gpt-4o"},
		Extensions: map[string]json.RawMessage{
			"openai.model": json.RawMessage(`"gpt-4o"`),
		},
		Session: lipapi.SessionRef{
			AuthoritativeSessionID: "auth-1",
			ClientSessionID:        "client-1",
			ALegID:                 "aleg-1",
			ResumeToken:            "secret-tok",
		},
	}
	wantSum := diag.StableCallSum(expectedCall)
	if digest.Sum() != wantSum {
		t.Fatalf("convenience fields sum mismatch:\ngot:  %x\nwant: %x", digest.Sum(), wantSum)
	}
}

// TestIdentityWriter_MultiPartAndMultiItemStreaming tests complex conversations
// with multiple parts and items.
func TestIdentityWriter_MultiPartAndMultiItemStreaming(t *testing.T) {
	t.Parallel()

	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub:gpt-4o"},
		Messages: []lipapi.Message{
			{
				Role: lipapi.RoleUser,
				Parts: []lipapi.Part{
					lipapi.TextPart("Part 1 text content"),
					lipapi.FilePart("file-ref-1", "application/pdf", "doc.pdf"),
					lipapi.TextPart("Part 3 text content"),
				},
			},
		},
	}

	wantSum := diag.StableCallSum(call)

	w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
		Route: call.Route,
	})
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}

	mw, err := w.BeginMessage(lipapi.RoleUser)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}

	// Stream part 1
	pw1, err := mw.BeginTextPart()
	if err != nil {
		t.Fatalf("BeginTextPart: %v", err)
	}
	if _, err := io.WriteString(pw1, "Part 1 text content"); err != nil {
		t.Fatalf("WriteString pw1: %v", err)
	}
	if err := pw1.Close(); err != nil {
		t.Fatalf("pw1.Close: %v", err)
	}

	// Add non-text part 2
	if err := mw.AddPart(call.Messages[0].Parts[1]); err != nil {
		t.Fatalf("AddPart 2: %v", err)
	}

	// Stream part 3
	pw3, err := mw.BeginTextPart()
	if err != nil {
		t.Fatalf("BeginTextPart 3: %v", err)
	}
	if _, err := io.WriteString(pw3, "Part 3 text content"); err != nil {
		t.Fatalf("WriteString pw3: %v", err)
	}
	if err := pw3.Close(); err != nil {
		t.Fatalf("pw3.Close: %v", err)
	}

	if err := mw.EndMessage(); err != nil {
		t.Fatalf("mw.EndMessage: %v", err)
	}

	digest, err := w.Digest()
	if err != nil {
		t.Fatalf("w.Digest: %v", err)
	}
	if digest.Sum() != wantSum {
		t.Fatalf("multi-part sum mismatch:\ngot:  %x\nwant: %x", digest.Sum(), wantSum)
	}
}
