package conformance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	refanthropic "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/anthropicmessages"
	refgemini "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/gemini"
	refopenaichat "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/openaichat"
	refopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/openairesponses"
	"google.golang.org/genai"
)

func TestConformance_TextOnly_roundTrip(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.TextViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			beSrv := NewSuccessRefBackend(t, cell.Backend, nil)
			exec := NewTestExecutor(t, cell.Backend, beSrv.URL, beSrv.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			got := nonStreamAssistantText(t, cell.Frontend, feSrv.URL, feSrv.Client())
			if cell.Backend == "acp" {
				if !strings.Contains(got, "ok") {
					t.Fatalf("expected ACP stock emulator text containing ok, got %q", got)
				}
				return
			}
			if !strings.Contains(got, parityText) {
				t.Fatalf("expected parity text in response, got %q", got)
			}
		})
	}
}

func TestConformance_TextOnly_streamAndNonStreamParity(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.TextViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			beSrv := NewSuccessRefBackend(t, cell.Backend, nil)
			exec := NewTestExecutor(t, cell.Backend, beSrv.URL, beSrv.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			ns := nonStreamAssistantText(t, cell.Frontend, feSrv.URL, feSrv.Client())
			st := streamAssistantText(t, cell.Frontend, feSrv.URL, feSrv.Client())
			if cell.Backend == "acp" {
				if !strings.Contains(ns, "ok") || !strings.Contains(st, "ok") {
					t.Fatalf("expected ok in both paths non-stream=%q stream=%q", ns, st)
				}
				return
			}
			if !strings.Contains(ns, parityText) || !strings.Contains(st, parityText) {
				t.Fatalf("expected parity in both paths non-stream=%q stream=%q", ns, st)
			}
		})
	}
}

func TestConformance_TextOnly_upstreamErrorShape(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.TextViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			up := NewUpstream400Server(t, cell.Backend)
			exec := NewTestExecutor(t, cell.Backend, up.URL, up.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			err := nonStreamExpectError(t, cell.Frontend, feSrv.URL, feSrv.Client())
			if err == nil {
				t.Fatal("expected upstream error")
			}
			switch cell.Frontend {
			case "openai-responses", "openai-legacy":
				var apiErr *openai.Error
				if !errors.As(err, &apiErr) {
					t.Fatalf("expected *openai.Error, got %T: %v", err, err)
				}
				if apiErr.StatusCode != http.StatusBadRequest && apiErr.StatusCode != http.StatusInternalServerError {
					t.Fatalf("status %d", apiErr.StatusCode)
				}
				if !clientVisibleErrorIndicatesFailure(apiErr.Error()) {
					t.Fatalf("expected sanitized or diagnostic client error text: %v", apiErr)
				}
			case "anthropic":
				var apiErr *anthropic.Error
				if !errors.As(err, &apiErr) {
					t.Fatalf("expected *anthropic.Error, got %T: %v", err, err)
				}
				if apiErr.StatusCode != http.StatusBadRequest && apiErr.StatusCode != http.StatusInternalServerError {
					t.Fatalf("status %d", apiErr.StatusCode)
				}
				if !clientVisibleErrorIndicatesFailure(apiErr.Error()) {
					t.Fatalf("expected sanitized or diagnostic client error text: %v", apiErr)
				}
			case "gemini":
				if err == nil {
					t.Fatal("expected error")
				}
				lower := strings.ToLower(err.Error())
				if !strings.Contains(lower, "400") && !strings.Contains(lower, "invalid") && !strings.Contains(lower, "internal error") {
					t.Fatalf("expected client-visible error mentioning status, invalid, or generic internal failure, got %v", err)
				}
			default:
				t.Fatalf("unexpected frontend %q", cell.Frontend)
			}
		})
	}
}

// clientVisibleErrorIndicatesFailure accepts either legacy upstream-shaped text or the generic
// proxy message returned for internal executor failures (no upstream echo on the wire).
func clientVisibleErrorIndicatesFailure(s string) bool {
	lower := strings.ToLower(s)
	if strings.Contains(lower, "internal error") {
		return true
	}
	return strings.Contains(lower, "400") ||
		strings.Contains(lower, "invalid") ||
		strings.Contains(lower, "validationexception") ||
		strings.Contains(lower, "invalid params")
}

func nonStreamExpectError(tb testing.TB, frontendID, proxyOrigin string, httpClient *http.Client) error {
	tb.Helper()
	ctx := context.Background()
	switch frontendID {
	case "openai-responses":
		cli := refopenairesponses.New(refopenairesponses.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
		_, err := cli.CreateResponse(ctx, responses.ResponseNewParams{
			Model: shared.ResponsesModel(wireModelForFrontend(frontendID)),
			Input: responses.ResponseNewParamsInputUnion{
				OfInputItemList: []responses.ResponseInputItemUnionParam{
					responses.ResponseInputItemParamOfMessage("ping", responses.EasyInputMessageRoleUser),
				},
			},
		})
		return err
	case "openai-legacy":
		cli := refopenaichat.New(refopenaichat.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
		_, err := cli.CreateChatCompletion(ctx, openai.ChatCompletionNewParams{
			Model: shared.ChatModelGPT4oMini,
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.UserMessage("ping"),
			},
		})
		return err
	case "anthropic":
		cli := refanthropic.New(refanthropic.Config{
			BaseURL:    proxyOrigin,
			APIKey:     "sk-ant-test",
			HTTPClient: httpClient,
		})
		_, err := cli.CreateMessage(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(wireModelForFrontend(frontendID)),
			MaxTokens: 64,
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("ping")),
			},
		})
		return err
	case "gemini":
		cli, err := refgemini.New(ctx, refgemini.Config{
			BaseURL:    proxyOrigin,
			APIKey:     "fake-key",
			HTTPClient: httpClient,
		})
		if err != nil {
			return err
		}
		_, err = cli.GenerateContent(ctx, wireModelForFrontend(frontendID), []*genai.Content{
			genai.NewContentFromText("ping", genai.RoleUser),
		}, nil)
		return err
	default:
		tb.Fatalf("unknown frontend %q", frontendID)
		return nil
	}
}
