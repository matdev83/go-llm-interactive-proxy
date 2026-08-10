package frontend

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/contract/semantic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

func TestBundledFrontends_CertifyIndependentlyWithCapturingExecutor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		mount funcFrontendMount
		path  string
		body  string
	}{
		{"openai-responses", openairesponses.Mount, "/v1/responses", `{"model":"m","input":"hi"}`},
		{"openresponses", openresponses.Mount, "/openresponses/v1/responses", `{"model":"m","input":"hi"}`},
		{"openai-legacy", openailegacy.Mount, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"anthropic", anthropic.Mount, "/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
		{"gemini", gemini.Mount, "/v1beta/models/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			executor := &CapturingExecutor{Script: EventScript{Events: []lipapi.Event{
				{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventTextDelta, Delta: "ok"}, {Kind: lipapi.EventResponseFinished},
			}}}
			caps := getFrontendCapabilities(tc.name)
			dialects := getFrontendDialects(tc.name)
			h := &MountedHarness{
				Descriptor: semantic.SubjectDescriptor{
					ID:           tc.name,
					Kind:         semantic.KindFrontend,
					Capabilities: caps,
					Dialects:     dialects,
					Transports:   []semantic.ScenarioTransport{semantic.TransportHTTP, semantic.TransportStreaming, semantic.TransportWebSocket},
				},
				Mount: tc.mount, Path: func(sc semantic.ScenarioDescriptor) string {
					if tc.name == "gemini" && (sc.Transport == semantic.TransportStreaming || sc.ID == "cancellation") {
						return "/v1beta/models/m:streamGenerateContent?alt=sse"
					}
					if tc.name == "openresponses" && sc.ID == "compaction-lifecycle" {
						return "/openresponses/v1/responses/compact"
					}
					return tc.path
				}, Body: func(sc semantic.ScenarioDescriptor) []byte {
					return []byte(frontendScenarioBody(tc.name, string(sc.ID)))
				}, ExecutorBoundary: executor, ContinuationStore: lipcont.NewMemoryStore(),
			}
			cert, err := CertifyFrontend(context.Background(), h)
			if err != nil {
				t.Fatal(err)
			}
			if err := cert.ValidateReleaseReady(); err != nil {
				t.Fatal(err)
			}
			if len(executor.Calls) == 0 {
				t.Fatal("frontend TCK captured no canonical calls")
			}
		})
	}
}

func getFrontendCapabilities(name string) []lipapi.Capability {
	switch name {
	case "openai-responses":
		return []lipapi.Capability{
			lipapi.CapabilityStreaming,
			lipapi.CapabilityTools,
			lipapi.CapabilityVision,
			lipapi.CapabilityDocuments,
		}
	case "openresponses":
		return []lipapi.Capability{
			lipapi.CapabilityStreaming,
			lipapi.CapabilityTools,
			lipapi.CapabilityVision,
			lipapi.CapabilityDocuments,
			lipapi.CapabilityStructuredOutputs,
			lipapi.CapabilityReasoning,
			lipapi.CapabilityOrderedItems,
			lipapi.CapabilityItemReferences,
			lipapi.CapabilityCompaction,
			lipapi.CapabilityOpaqueExtensions,
			lipapi.CapabilityReasoningReplay,
		}
	case "anthropic":
		return []lipapi.Capability{
			lipapi.CapabilityStreaming,
			lipapi.CapabilityTools,
			lipapi.CapabilityVision,
			lipapi.CapabilityDocuments,
		}
	case "openai-legacy":
		return []lipapi.Capability{
			lipapi.CapabilityStreaming,
			lipapi.CapabilityTools,
		}
	case "gemini":
		return []lipapi.Capability{
			lipapi.CapabilityStreaming,
			lipapi.CapabilityTools,
		}
	default:
		return []lipapi.Capability{
			lipapi.CapabilityStreaming,
			lipapi.CapabilityTools,
		}
	}
}

func getFrontendDialects(name string) lipapi.DialectSupport {
	if name == "openresponses" {
		return lipapi.DialectSupport{
			ItemDialects: []lipapi.DialectRequirement{
				{Kind: "item", Dialect: "item_reference"},
			},
			ReasoningDialects: []lipapi.DialectRequirement{
				{Kind: "reasoning", Dialect: "reasoning_replay"},
			},
			ExtensionTypes: []lipapi.ExtensionRequirement{
				{Namespace: "com.example", Type: "custom"},
			},
		}
	}
	return lipapi.DialectSupport{}
}

func frontendScenarioBody(frontend, scenario string) string {
	legacy := frontend == "openai-legacy"
	anthropic := frontend == "anthropic"
	gemini := frontend == "gemini"
	if scenario == "text-baseline" || scenario == "text-streaming" || scenario == "usage-present" || scenario == "usage-zero" || scenario == "recoverable-error" || scenario == "terminal-error" || scenario == "cancellation" {
		if anthropic {
			return `{"model":"m","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		}
		if gemini {
			return `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
		}
		if legacy {
			return `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
		}
		return `{"model":"m","input":"hi","stream":true}`
	}
	if anthropic {
		switch scenario {
		case "tools-execution", "tool-call-replay", "tool-result-replay":
			return `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"weather","input_schema":{"type":"object"}}]}`
		case "vision-input":
			return `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}]}`
		case "documents-input":
			return `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"document","source":{"type":"base64","media_type":"text/plain","data":"SGVsbG8="}}]}]}`
		case "reasoning-output":
			return `{"model":"m","max_tokens":16,"thinking":{"type":"enabled","budget_tokens":8},"messages":[{"role":"user","content":"think"}]}`
		case "structured-output":
			return `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"json"}],"metadata":{"output_format":"json"}}`
		default:
			return `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
		}
	}
	if gemini {
		if scenario == "tools-execution" || scenario == "tool-call-replay" || scenario == "tool-result-replay" {
			return `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"weather","parameters":{"type":"object"}}]}]}`
		}
		return `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	}
	if legacy {
		if scenario == "tools-execution" || scenario == "tool-call-replay" || scenario == "tool-result-replay" {
			return `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}]}`
		}
		return `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	}
	switch scenario {
	case "tools-execution", "tool-call-replay", "tool-result-replay":
		return `{"model":"m","input":"hi","tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}]}`
	case "vision-input":
		return `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="}]}]}`
	case "documents-input":
		return `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_file","file_data":"SGVsbG8=","filename":"note.txt"}]}]}`
	case "structured-output":
		return `{"model":"m","input":"json","text":{"format":{"type":"json_object"}}}`
	case "reasoning-output":
		return `{"model":"m","input":"think","reasoning":{"effort":"medium"}}`
	case "compaction-lifecycle":
		if frontend == "openresponses" {
			return `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"compact"}]}]}`
		}
		return `{"model":"m","input":"compact"}`
	case "ordered-items":
		return `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	case "item-reference-dialect":
		return `{"model":"m","input":[{"type":"item_reference","id":"msg_prev"}]}`
	case "opaque-extension-type":
		if frontend == "openresponses" {
			return `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"com.example:custom","data":{}}]}]}`
		}
		return `{"model":"m","input":"hi"}`
	case "reasoning-replay-dialect":
		if frontend == "openresponses" {
			return `{"model":"m","input":[{"type":"reasoning","id":"reas_1","reasoning":"Thinking step..."}]}`
		}
		return `{"model":"m","input":"hi"}`
	default:
		return `{"model":"m","input":[{"type":"unknown_semantic_item"}]}`
	}
}

type funcFrontendMount = lipsdk.FrontendMount
