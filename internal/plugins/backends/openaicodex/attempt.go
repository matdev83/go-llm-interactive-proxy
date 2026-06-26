package openaicodex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/streampeek"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type codexOpenEnv struct {
	payload       Payload
	originalModel string
	convID        string
	client        *http.Client
	endpoint      string
	downgrade     downgradePolicy
}

func prepareCodexOpenEnv(ctx context.Context, cfg *Config, call lipapi.Call, cand routing.AttemptCandidate, policy downgradePolicy) (*codexOpenEnv, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%s: %w", ID, lipapi.ErrNilContext)
	}
	payload, err := PayloadForCall(&call, cand, *cfg)
	if err != nil {
		return nil, err
	}
	originalModel := strings.TrimSpace(cand.Primary.Model)
	convID := conversationID(call, originalModel)
	payload.PromptCacheKey = convID
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &codexOpenEnv{
		payload:       payload,
		originalModel: originalModel,
		convID:        convID,
		client:        client,
		endpoint:      responsesEndpoint(cfg.BaseURL),
		downgrade:     policy,
	}, nil
}

func (env *codexOpenEnv) marshalWithModel(model string) ([]byte, error) {
	env.payload.Model = model
	body, err := json.Marshal(env.payload)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal payload: %w", ID, err)
	}
	return body, nil
}

func (env *codexOpenEnv) newAttempt(ctx context.Context, cfg *Config, call lipapi.Call, body []byte, usageEst *usageEstimator) *codexOpenAttempt {
	payload := env.payload
	return &codexOpenAttempt{
		ctx:           ctx,
		cfg:           cfg,
		call:          call,
		client:        env.client,
		endpoint:      env.endpoint,
		convID:        env.convID,
		originalModel: env.originalModel,
		payload:       &payload,
		body:          body,
		downgrade:     env.downgrade,
		usageEst:      usageEst,
	}
}

func completeCodexOpenAttempt(attempt *codexOpenAttempt, resp *http.Response, callCfg *Config) (lipapi.ManagedEventStream, *http.Response, error) {
	resp, err := attempt.maybeRetryGPT55Downgrade(resp, callCfg)
	if err != nil {
		return nil, nil, err
	}
	if err := non2xxOrNil(resp); err != nil {
		return nil, nil, err
	}
	es, err := attempt.openStream(resp)
	if err != nil {
		return nil, nil, err
	}
	return es, resp, nil
}

type codexOpenAttempt struct {
	ctx              context.Context
	cfg              *Config
	call             lipapi.Call
	client           *http.Client
	endpoint         string
	convID           string
	originalModel    string
	payload          *Payload
	body             []byte
	downgrade        downgradePolicy
	usageEst         *usageEstimator
	downgradeRetried bool
}

func readLimitedClose(resp *http.Response) []byte {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	_ = resp.Body.Close()
	return b
}

func upstreamHTTPError(status int, body []byte) error {
	return fmt.Errorf("%s: upstream HTTP %d: %s", ID, status, strings.TrimSpace(string(body)))
}

func non2xxOrNil(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	return upstreamHTTPError(resp.StatusCode, readLimitedClose(resp))
}

func (a *codexOpenAttempt) doRequest(callCfg *Config) (*http.Response, error) {
	return doCodexRequest(a.ctx, a.client, a.endpoint, a.body, callCfg, a.convID)
}

func (a *codexOpenAttempt) maybeRetryGPT55Downgrade(resp *http.Response, callCfg *Config) (*http.Response, error) {
	if resp.StatusCode != http.StatusBadRequest {
		return resp, nil
	}
	b := readLimitedClose(resp)
	retryBody, ok, rerr := a.downgrade.retryBody(a.originalModel, a.downgradeRetried, resp.StatusCode, string(b), a.payload)
	if rerr != nil {
		return nil, rerr
	}
	if !ok {
		return nil, upstreamHTTPError(resp.StatusCode, b)
	}
	a.body = retryBody
	a.downgradeRetried = true
	resp, err := a.doRequest(callCfg)
	if err != nil {
		return nil, err
	}
	if err := non2xxOrNil(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (a *codexOpenAttempt) openStream(resp *http.Response) (lipapi.ManagedEventStream, error) {
	model := strings.TrimSpace(a.payload.Model)
	if model == "" {
		model = a.originalModel
	}
	es := newCodexStream(resp.Body, a.call.MaxPendingWireEvents)
	managed := newUsageEstimatingStream(es, a.usageEst, a.call, model)
	ev, rerr := managed.Recv(a.ctx)
	if rerr == nil {
		return streampeek.NewManagedPrependFirst(ev, managed), nil
	}
	_ = managed.Close()
	return nil, rerr
}
