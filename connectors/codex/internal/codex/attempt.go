package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/routingstub"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/streampeek"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type codexOpenEnv struct {
	payload           Payload
	originalModel     string
	convID            string
	turnKey           string
	turnNo            int
	inputFingerprints []string
	client            *http.Client
	endpoint          string
	downgrade         downgradePolicy
	turns             *sessionTurnCounter
	turnReserved      bool
	markerEligible    bool
	nativeOriginal    Payload
	nativeOriginalFP  []string
	nativeUsage       *NativeUsageEvidence
}

func prepareCodexOpenEnv(ctx context.Context, cfg *Config, call lipapi.Call, cand routingstub.AttemptCandidate, policy downgradePolicy, turns *sessionTurnCounter) (*codexOpenEnv, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%s: %w", ID, lipapi.ErrNilContext)
	}
	markerEligible := peekNativeContinuityMarker(&call)
	payload, err := PayloadForCall(&call, cand, *cfg)
	if err != nil {
		return nil, err
	}
	originalModel := normalizeCodexModel(cand.Primary.Model)
	inputFingerprints := fingerprintInputItems(payload.Input)
	convID := conversationIDForPayloadWithFingerprints(call, originalModel, payload, inputFingerprints)
	turnKey := sessionTurnKey(call, convID)
	payload.PromptCacheKey = convID
	turnNo := 0
	reserved := false
	if turns != nil && verbosityTurnsEnabled(*cfg) {
		turnNo = turns.reserveTurn(turnKey)
		reserved = turnNo > 0
	}
	applyEarlySessionVerbosityBump(&payload, call, *cfg, turnNo)
	applyMidSessionVerbosityBump(&payload, call, *cfg, turnNo)
	logPayloadShape(ctx, &call, payload)
	nativeOriginal, err := clonePayload(payload)
	if err != nil {
		return nil, fmt.Errorf("%s: clone native context baseline: %w", ID, err)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &codexOpenEnv{
		payload:           payload,
		originalModel:     originalModel,
		convID:            convID,
		turnKey:           turnKey,
		turnNo:            turnNo,
		inputFingerprints: inputFingerprints,
		client:            client,
		endpoint:          responsesEndpoint(cfg.BaseURL),
		downgrade:         policy,
		turns:             turns,
		turnReserved:      reserved,
		markerEligible:    markerEligible,
		nativeOriginal:    nativeOriginal,
		nativeOriginalFP:  append([]string(nil), inputFingerprints...),
	}, nil
}

func (env *codexOpenEnv) prepareNativeContext(ctx context.Context, cfg *Config, call lipapi.Call, model string, coordinator *NativeContextCoordinator, onCommit func()) error {
	if env == nil || coordinator == nil {
		return nil
	}
	// Managed account rotation and pre-output auth refresh can call preparation
	// more than once. Always start from the immutable pre-native baseline so an
	// earlier account cannot donate a rewritten input or response-id state.
	baseline, err := clonePayload(env.nativeOriginal)
	if err != nil {
		return fmt.Errorf("%s: clone native context baseline: %w", ID, err)
	}
	env.payload = baseline
	env.inputFingerprints = append([]string(nil), env.nativeOriginalFP...)
	// A managed-account/auth retry rebuilds the attempt under a new identity.
	// Never carry provider usage evidence from the losing account into the next
	// account's surfaced stream.
	env.nativeUsage = nil
	prepared, err := coordinator.Prepare(ctx, NativeContextPrepareInput{
		Call: call, Payload: env.payload, Account: *cfg, Model: model,
		MarkerEligible: env.markerEligible, ClientFamily: continuationClientFamily(call), ConversationID: env.convID,
		OnCheckpointCommit: onCommit,
	})
	if err != nil {
		return err
	}
	env.payload = prepared.Payload
	env.inputFingerprints = fingerprintInputItems(env.payload.Input)
	// A normal-request/account retry may reuse a checkpoint after a previous
	// compaction already completed. Keep that pending billable evidence until a
	// successful normal stream can publish it; reuse itself is not a new charge.
	if prepared.CompactionUsage != nil {
		env.nativeUsage = cloneUsageEvidence(prepared.CompactionUsage)
	}
	return nil
}

func cloneUsageEvidence(src *NativeUsageEvidence) *NativeUsageEvidence {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

func (env *codexOpenEnv) wrapNativeUsage(stream lipapi.ManagedEventStream) lipapi.ManagedEventStream {
	if env == nil || stream == nil || env.nativeUsage == nil {
		return stream
	}
	usage := cloneUsageEvidence(env.nativeUsage)
	// Consume the prepared evidence at the stream boundary. A managed-account
	// retry rebuilds env from its immutable baseline and gets its own evidence;
	// Recv retries and Close cannot emit this evidence a second time.
	env.nativeUsage = nil
	return newNativeUsageStream(stream, usage)
}

// releaseVerbosityTurn undoes a turn reserved during prepare when the open
// ultimately fails. Successful opens must call commitVerbosityTurn instead.
func (env *codexOpenEnv) releaseVerbosityTurn() {
	if env == nil || !env.turnReserved || env.turns == nil {
		return
	}
	env.turns.releaseTurn(env.turnKey, env.turnNo)
	env.turnReserved = false
}

// commitVerbosityTurn marks a reserved turn as successfully completed so
// in-flight TTL/eviction protection can clear while keeping the turn consumed.
func (env *codexOpenEnv) commitVerbosityTurn() {
	if env == nil || !env.turnReserved || env.turns == nil {
		return
	}
	env.turns.commitTurn(env.turnKey, env.turnNo)
	env.turnReserved = false
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

const upstreamErrorBodyMax = 256

// truncateErrorMessage bounds upstream/OAuth response text embedded in errors
// so provider error bodies cannot dump multi-KiB of (possibly echoed) content
// into logs.
func truncateErrorMessage(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("…(truncated %d bytes)", len(s)-max)
}

func upstreamHTTPError(status int, body []byte) error {
	return fmt.Errorf("%s: upstream HTTP %d: %s", ID, status, truncateErrorMessage(string(body), upstreamErrorBodyMax))
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
	st := newCodexStream(resp.Body, a.call.MaxPendingWireEvents)
	managed, err := openManagedFirstEvent(a.ctx, st, a.usageEst, a.call, model)
	if err != nil {
		return nil, err
	}
	return managed, nil
}

func openManagedFirstEvent(ctx context.Context, es lipapi.ManagedEventStream, usageEst *usageEstimator, call lipapi.Call, model string) (lipapi.ManagedEventStream, error) {
	managed := newUsageEstimatingStream(es, usageEst, call, model)
	start := time.Now()
	ev, rerr := managed.Recv(ctx)
	logFirstEventWait(ctx, call, model, start, ev, rerr)
	if rerr == nil {
		return streampeek.NewManagedPrependFirst(ev, managed), nil
	}
	_ = managed.Close()
	return nil, rerr
}
