package openresponses_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type memSource struct {
	data []byte
}

func newMemSource(data []byte) *memSource {
	return &memSource{data: data}
}

func (s *memSource) Size() int64 {
	return int64(len(s.data))
}

func (s *memSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

func (s *memSource) Close() error {
	return nil
}

func defaultOpenResponsesProofInput(body []byte, routeSelector string, headers http.Header) frontendpipe.ProofInput {
	return frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		Headers:              headers,
		URLPath:              "/openresponses/v1/responses",
		RouteSelector:        routeSelector,
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"openai", "stub", "custom"}),
		DefaultRouteSelector: "stub:default",
		RouteFromBodyModel:   true,
		Source:               newMemSource(body),
		BodyBytes:            int64(len(body)),
	}
}

// Requirement 13, 17.6: Confirm at implementation time: no legacy full-body resolver; RouteFromBodyModel=true.
func TestOpenResponsesProfile_PrerequisitesAndIdentity(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()
	if prof.ProfileID() != openresponses.ProfileID {
		t.Fatalf("ProfileID: got %q, want %q", prof.ProfileID(), openresponses.ProfileID)
	}
	if prof.ProfileID() != "openresponses_v1" {
		t.Fatalf("ProfileID constant: got %q, want openresponses_v1", prof.ProfileID())
	}

	// Verify Handler builds pipe with no legacy ResolveRouteSelector
	h := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req) // triggers buildPipe via pipeOnce

	spec := h.Spec()
	if spec == nil {
		t.Fatal("Handler.Spec() returned nil")
	}
	if spec.ResolveRouteSelector != nil {
		t.Fatal("Handler must NOT configure legacy ResolveRouteSelector (Requirement 13.2, 17.6)")
	}
	if !spec.RouteFromBodyModel {
		t.Fatal("Handler must configure RouteFromBodyModel=true (Requirement 4.8, 17.6)")
	}

	// Injected profile is honored via HandlerConfig (Task 17 seam)
	customProf := openresponses.NewProfile()
	h2 := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Profile:              customProf,
	})
	req2 := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{}`))
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req2)
	if h2.Spec().Profile != customProf {
		t.Fatal("Handler must honor explicit Profile injection")
	}
	if h2.Spec().Profile.ProfileID() != "openresponses_v1" {
		t.Fatalf("Handler pipe profile ID: got %q, want openresponses_v1", h2.Spec().Profile.ProfileID())
	}
}

// Requirement 4, 14, 16, 17: Compile proof for simple no-store create request.
func TestOpenResponsesProfile_CompileProof_SimpleNoStoreCreate(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","input":"hello world","store":false}`)
	prof := openresponses.NewProfile()
	in := defaultOpenResponsesProofInput(body, "gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.State.Proof
	if proof.ProfileID != "openresponses_v1" {
		t.Fatalf("ProfileID: got %q, want openresponses_v1", proof.ProfileID)
	}
	if proof.Operation != lipapi.OperationOpenResponsesCreate {
		t.Fatalf("Operation: got %q, want %q", proof.Operation, lipapi.OperationOpenResponsesCreate)
	}
	if proof.Delivery != lipapi.DeliveryModeNonStreaming {
		t.Fatalf("Delivery: got %v, want non-streaming", proof.Delivery)
	}
	if proof.ClientModel != "gpt-4o" {
		t.Fatalf("ClientModel: got %q, want gpt-4o", proof.ClientModel)
	}
	if proof.RouteSelector != "gpt-4o" {
		t.Fatalf("RouteSelector: got %q, want gpt-4o", proof.RouteSelector)
	}
	if proof.Mode != largebody.BodyModeIdentityJSON {
		t.Fatalf("BodyMode: got %v, want IdentityJSON", proof.Mode)
	}
	if !proof.Rewrite.NeedsModelRewrite() {
		t.Fatalf("Rewrite.NeedsModelRewrite: got false, want true")
	}

	// Verify exact model span
	if proof.ModelSpan.Length == 0 {
		t.Fatal("ModelSpan length is 0")
	}
	spanBytes := body[proof.ModelSpan.Offset : proof.ModelSpan.Offset+proof.ModelSpan.Length]
	if string(spanBytes) != `"gpt-4o"` {
		t.Fatalf("ModelSpan text: got %q, want %q", string(spanBytes), `"gpt-4o"`)
	}

	// Verify seeds
	seeds := out.State.Seeds
	if seeds.DeterministicToken != proof.Identity.Token() {
		t.Fatal("Seeds token must match proof identity token")
	}
	if seeds.ClientModel != "gpt-4o" {
		t.Fatalf("Seeds ClientModel: got %q, want gpt-4o", seeds.ClientModel)
	}
	if seeds.RouteSelector != "gpt-4o" {
		t.Fatalf("Seeds RouteSelector: got %q, want gpt-4o", seeds.RouteSelector)
	}
	if seeds.Stream {
		t.Fatal("Seeds Stream: got true, want false")
	}
}

// Requirement 4, 14, 16, 17: Compile proof with streaming, item array, options.
func TestOpenResponsesProfile_CompileProof_StreamingAndOptions(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"streaming test"}]}],"stream":true,"store":false,"temperature":0.7,"top_p":0.9,"max_output_tokens":100}`)
	prof := openresponses.NewProfile()
	in := defaultOpenResponsesProofInput(body, "", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.State.Proof
	if proof.Delivery != lipapi.DeliveryModeStreaming {
		t.Fatalf("Delivery: got %v, want streaming", proof.Delivery)
	}
	if proof.MaxOutputTokens != 100 {
		t.Fatalf("MaxOutputTokens: got %d, want 100", proof.MaxOutputTokens)
	}
	if !out.State.Seeds.Stream {
		t.Fatal("Seeds Stream: got false, want true")
	}
}

// Requirement 17.4: Explicit store:false gate.
// store:true, missing store, previous_response_id, truncation, background,
// unsupported controls, unknown keys, duplicate keys must decline to canonical.
func TestOpenResponsesProfile_CompileProof_ExplicitStoreFalseGate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		json string
	}{
		{
			name: "missing store defaults to true",
			json: `{"model":"gpt-4o","input":"hello"}`,
		},
		{
			name: "explicit store true",
			json: `{"model":"gpt-4o","input":"hello","store":true}`,
		},
		{
			name: "store string value",
			json: `{"model":"gpt-4o","input":"hello","store":"false"}`,
		},
		{
			name: "store null value",
			json: `{"model":"gpt-4o","input":"hello","store":null}`,
		},
		{
			name: "previous_response_id present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"previous_response_id":"resp_MDEyMzQ1Njc4OWFiY2RlZg"}`,
		},
		{
			name: "truncation present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"truncation":"auto"}`,
		},
		{
			name: "background true",
			json: `{"model":"gpt-4o","input":"hello","store":false,"background":true}`,
		},
		{
			name: "include present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"include":["message.output_text"]}`,
		},
		{
			name: "presence_penalty present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"presence_penalty":0.5}`,
		},
		{
			name: "frequency_penalty present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"frequency_penalty":0.5}`,
		},
		{
			name: "stream_options present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"stream_options":{"include_usage":true}}`,
		},
		{
			name: "top_logprobs present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"top_logprobs":3}`,
		},
		{
			name: "service_tier present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"service_tier":"default"}`,
		},
		{
			name: "safety_identifier present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"safety_identifier":"safe-1"}`,
		},
		{
			name: "prompt_cache_key present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"prompt_cache_key":"k1"}`,
		},
		{
			name: "prompt_cache_retention present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"prompt_cache_retention":"in_memory"}`,
		},
		{
			name: "max_tool_calls present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"max_tool_calls":2}`,
		},
		{
			name: "unknown top-level key",
			json: `{"model":"gpt-4o","input":"hello","store":false,"custom_field_x":123}`,
		},
		{
			name: "duplicate key model",
			json: `{"model":"gpt-4o","model":"gpt-4o","input":"hello","store":false}`,
		},
		{
			name: "empty model",
			json: `{"model":"","input":"hello","store":false}`,
		},
		{
			name: "empty input",
			json: `{"model":"gpt-4o","input":"","store":false}`,
		},
	}

	prof := openresponses.NewProfile()
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := defaultOpenResponsesProofInput([]byte(tc.json), "", nil)
			_, err := prof.CompileProof(context.Background(), in)
			if err == nil {
				t.Fatalf("expected CompileProof to fail and decline to canonical, got nil error for %s", tc.name)
			}
		})
	}
}

// Requirement 17.4: Path verification - only create path allowed.
func TestOpenResponsesProfile_CompileProof_PathVerification(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","input":"hello","store":false}`)
	prof := openresponses.NewProfile()

	t.Run("compact path rejected", func(t *testing.T) {
		t.Parallel()
		in := defaultOpenResponsesProofInput(body, "", nil)
		in.URLPath = "/openresponses/v1/responses/compact"
		_, err := prof.CompileProof(context.Background(), in)
		if err == nil {
			t.Fatal("expected error for compact path")
		}
	})

	t.Run("non-responses path rejected", func(t *testing.T) {
		t.Parallel()
		in := defaultOpenResponsesProofInput(body, "", nil)
		in.URLPath = "/openresponses/v1/models"
		_, err := prof.CompileProof(context.Background(), in)
		if err == nil {
			t.Fatal("expected error for non-responses path")
		}
	})

	t.Run("short responses path accepted", func(t *testing.T) {
		t.Parallel()
		in := defaultOpenResponsesProofInput(body, "", nil)
		in.URLPath = "/responses"
		_, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error for /responses path: %v", err)
		}
	})
}

// Requirement 4.8, 17.6: Route selector precedence.
func TestOpenResponsesProfile_CompileProof_RouteSelectorPrecedence(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	t.Run("explicit header selector wins", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"openai:gpt-4o","input":"hello","store":false}`)
		in := defaultOpenResponsesProofInput(body, "custom:override", nil)
		out, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("CompileProof failed: %v", err)
		}
		if out.State.Proof.RouteSelector != "custom:override" {
			t.Fatalf("RouteSelector: got %q, want custom:override", out.State.Proof.RouteSelector)
		}
	})

	t.Run("body model used when header selector empty", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"openai:gpt-4o","input":"hello","store":false}`)
		in := defaultOpenResponsesProofInput(body, "", nil)
		out, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("CompileProof failed: %v", err)
		}
		if out.State.Proof.RouteSelector != "openai:gpt-4o" {
			t.Fatalf("RouteSelector: got %q, want openai:gpt-4o", out.State.Proof.RouteSelector)
		}
	})

	t.Run("default route selector applied for unprefixed model when prefixes configured", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","input":"hello","store":false}`)
		in := defaultOpenResponsesProofInput(body, "", nil)
		out, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("CompileProof failed: %v", err)
		}
		if out.State.Proof.RouteSelector != "stub:default" {
			t.Fatalf("RouteSelector: got %q, want stub:default", out.State.Proof.RouteSelector)
		}
	})
}

// Requirement 14.2, 16.2: Session input, body metadata rejection, client session hint.
func TestOpenResponsesProfile_CompileProof_SessionInputAndHeaders(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	t.Run("authoritative session headers mapped", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","input":"hello","store":false}`)
		hdr := make(http.Header)
		hdr.Set("X-LIP-Session-ID", "sess_authoritative_123")
		hdr.Set("X-LIP-A-Leg-ID", "aleg_456")

		in := defaultOpenResponsesProofInput(body, "", hdr)
		out, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("CompileProof failed: %v", err)
		}
		if out.State.Proof.Session.AuthoritativeSessionID != "sess_authoritative_123" {
			t.Fatalf("AuthoritativeSessionID: got %q, want sess_authoritative_123", out.State.Proof.Session.AuthoritativeSessionID)
		}
		if out.State.Proof.Session.ALegID != "aleg_456" {
			t.Fatalf("ALegID: got %q, want aleg_456", out.State.Proof.Session.ALegID)
		}
	})

	t.Run("body session metadata rejected", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","input":"hello","store":false,"metadata":{"lip_session_id":"sess_spoofed"}}`)
		in := defaultOpenResponsesProofInput(body, "", nil)
		_, err := prof.CompileProof(context.Background(), in)
		if err == nil {
			t.Fatal("expected error for body session metadata")
		}
		if !errors.Is(err, largebody.ErrBodySessionMetadataRejected) {
			t.Fatalf("expected ErrBodySessionMetadataRejected, got %v", err)
		}
	})

	t.Run("client session ID hint declines to canonical", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","input":"hello","store":false}`)
		hdr := make(http.Header)
		hdr.Set("X-LIP-Session-Hint", "hint_789")

		in := defaultOpenResponsesProofInput(body, "", hdr)
		_, err := prof.CompileProof(context.Background(), in)
		if err == nil {
			t.Fatal("expected error for client session hint")
		}
		if !strings.Contains(err.Error(), "session hint requires canonical decode") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

// Requirement 16: Exact canonical semantic identity parity with CanonicalCallIdentity.
func TestOpenResponsesProfile_CompileProof_IdentityParity(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	cases := []struct {
		name string
		json string
	}{
		{
			name: "string input",
			json: `{"model":"gpt-4o","input":"hello world","store":false}`,
		},
		{
			name: "item array input",
			json: `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello item"}]}],"store":false}`,
		},
		{
			name: "with temperature and top_p",
			json: `{"model":"gpt-4o","input":"options test","store":false,"temperature":0.5,"top_p":0.8}`,
		},
		{
			name: "with instructions",
			json: `{"model":"gpt-4o","input":"test","instructions":"be helpful","store":false}`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(tc.json)
			in := defaultOpenResponsesProofInput(body, "", nil)
			out, err := prof.CompileProof(context.Background(), in)
			if err != nil {
				t.Fatalf("CompileProof failed: %v", err)
			}

			// Also decode via canonical proto.DecodeRequest to compare identity
			// Note: stream control is stripped for canonical create if present
			decodeBody := body
			withoutStream, _, err := proto.DecodeRequest(decodeBody, proto.DefaultLimits())
			_ = withoutStream
			if err != nil {
				t.Fatalf("canonical DecodeRequest failed: %v", err)
			}

			// Wire canonicalCall selector to match
			_, call, err := proto.DecodeRequest(decodeBody, proto.DefaultLimits())
			if err != nil {
				t.Fatalf("canonical DecodeRequest: %v", err)
			}
			call.Route = lipapi.RouteIntent{Selector: out.State.Proof.RouteSelector}
			call.Invocation = lipapi.Invocation{
				Operation:    lipapi.OperationOpenResponsesCreate,
				DeliveryMode: out.State.Proof.Delivery,
			}
			canonicalDigest := largebody.CanonicalCallIdentity(&call)
			if out.State.Proof.Identity.Sum() != canonicalDigest.Sum() {
				t.Fatalf("Identity digest mismatch:\n got:  %x\n want: %x", out.State.Proof.Identity.Sum(), canonicalDigest.Sum())
			}
		})
	}
}

// Requirements 4.8, 16.2, 17.6: Selector differential test proving wire Proof.RouteSelector
// equals canonical decode's selector for prefixed body model with no route header
// (both prefix-configured and empty-prefixes cases), and confirming header-selector wins.
func TestOpenResponsesProfile_SelectorDifferential(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                 string
		routePrefixes        []string
		defaultRouteSelector string
		headerSelector       string
		bodyJSON             string
		wantSelector         string
	}{
		{
			name:                 "prefixed body model with no route header, prefix-configured",
			routePrefixes:        []string{"stub", "openai"},
			defaultRouteSelector: "stub:default",
			headerSelector:       "",
			bodyJSON:             `{"model":"stub:gpt-4o","input":"hello differential","store":false}`,
			wantSelector:         "stub:gpt-4o",
		},
		{
			name:                 "prefixed body model with no route header, empty-prefixes",
			routePrefixes:        nil,
			defaultRouteSelector: "stub:default",
			headerSelector:       "",
			bodyJSON:             `{"model":"stub:gpt-4o","input":"hello differential","store":false}`,
			wantSelector:         "stub:gpt-4o",
		},
		{
			name:                 "header selector wins over body model (precedence header > body > default)",
			routePrefixes:        []string{"stub", "custom"},
			defaultRouteSelector: "stub:default",
			headerSelector:       "custom:override-model",
			bodyJSON:             `{"model":"stub:gpt-4o","input":"hello differential","store":false}`,
			wantSelector:         "custom:override-model",
		},
		{
			name:                 "non-matching body model falls back to default route",
			routePrefixes:        []string{"stub"},
			defaultRouteSelector: "stub:default",
			headerSelector:       "",
			bodyJSON:             `{"model":"other:gpt-4o","input":"hello differential","store":false}`,
			wantSelector:         "stub:default",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true,
				Profile:              openresponses.NewProfile(),
				RoutePrefixes:        tc.routePrefixes,
				DefaultRouteSelector: tc.defaultRouteSelector,
			})

			body := []byte(tc.bodyJSON)
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
			if tc.headerSelector != "" {
				req.Header.Set("X-LIP-Route", tc.headerSelector)
			}

			// Trigger buildPipe via Spec()
			spec := h.Spec()
			if spec == nil {
				t.Fatal("Handler.Spec() returned nil")
			}

			// 1. Wire Candidate Proof path (under spec flags)
			proofIn := frontendpipe.ProofInput{
				Ctx:                  context.Background(),
				Headers:              req.Header,
				URLPath:              req.URL.Path,
				RouteSelector:        spec.HTTPHeaders.RouteSelector(req.Header),
				RoutePrefixes:        spec.RoutePrefixes,
				DefaultRouteSelector: spec.DefaultRouteSelector,
				RouteFromBodyModel:   spec.RouteFromBodyModel,
				Source:               newMemSource(body),
				BodyBytes:            int64(len(body)),
			}

			proofOut, err := spec.Profile.CompileProof(context.Background(), proofIn)
			if err != nil {
				t.Fatalf("CompileProof failed: %v", err)
			}
			wireProof := proofOut.State.Proof

			// 2. Canonical Decode path
			dctx := frontendpipe.DecodeContext{
				Ctx:           context.Background(),
				Body:          body,
				RouteSelector: spec.HTTPHeaders.RouteSelector(req.Header),
				Headers:       req.Header,
				URLPath:       req.URL.Path,
			}
			canonDecoded, err := spec.Decode(dctx)
			if err != nil {
				t.Fatalf("canonical Decode failed: %v", err)
			}

			// Assert wire Proof.RouteSelector equals canonical decode's selector
			if wireProof.RouteSelector != canonDecoded.RouteSelector {
				t.Fatalf("RouteSelector differential mismatch:\n wire:      %q\n canonical: %q",
					wireProof.RouteSelector, canonDecoded.RouteSelector)
			}

			// Assert both equal the expected selector
			if wireProof.RouteSelector != tc.wantSelector {
				t.Fatalf("RouteSelector: got %q, want %q", wireProof.RouteSelector, tc.wantSelector)
			}

			// Assert identity parity between wire Proof and canonical Call (Requirement 16.2)
			canonDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
			if wireProof.Identity.Sum() != canonDigest.Sum() {
				t.Fatalf("Identity digest differential mismatch (Req 16.2):\n wire:      %x\n canonical: %x",
					wireProof.Identity.Sum(), canonDigest.Sum())
			}
		})
	}
}
