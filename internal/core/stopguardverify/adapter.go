package stopguardverify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/lineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

// PluginID is the verifier plugin identifier used for recursion suppression.
const PluginID = "agent_loop_guard"

// Visibility for the auxiliary verifier request.
const Visibility = "private"

// VerifyObservation reports verifier usage and latency for accounting/observability.
// It is passed to AdapterConfig.Observer exactly once per Verify call.
type VerifyObservation struct {
	Latency       time.Duration
	InputTokens   int
	OutputTokens  int
	TotalTokens   int
	CostNanoUnits int64
	Err           error
	ParentTraceID string
	ParentALegID  string
	ParentBLegID  string
}

// AdapterConfig holds verifier adapter construction parameters.
type AdapterConfig struct {
	Role    string
	Timeout time.Duration
	// Observer, if non-nil, is invoked exactly once per Verify call with
	// latency and usage from the auxiliary Collect. It must be fast and
	// non-blocking; the verifier does not wait for observer work.
	Observer func(VerifyObservation)
}

// Adapter implements stopguard.Verifier via an auxiliary client.
type Adapter struct {
	client auxiliary.Client
	cfg    AdapterConfig
}

// NewAdapter creates a verifier adapter.
func NewAdapter(client auxiliary.Client, cfg AdapterConfig) *Adapter {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 4 * time.Second
	}
	if strings.TrimSpace(cfg.Role) == "" {
		cfg.Role = "loop_guard"
	}
	return &Adapter{client: client, cfg: cfg}
}

// Verify runs the semantic completion check with bounded instruction/evidence.
func (a *Adapter) Verify(ctx context.Context, evidence stopguard.Evidence) (stopguard.Verdict, error) {
	var obs VerifyObservation
	defer func() {
		if a.cfg.Observer != nil {
			a.cfg.Observer(obs)
		}
	}()
	if err := ctx.Err(); err != nil {
		obs.Err = err
		return stopguard.Verdict{Kind: stopguard.VerdictUncertain}, err
	}
	// Bounded deadline.
	dctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
	defer cancel()
	if err := dctx.Err(); err != nil {
		obs.Err = err
		return stopguard.Verdict{Kind: stopguard.VerdictUncertain}, err
	}

	// Project evidence and build instruction (single user message, no tools).
	evidenceBlock := ProjectEvidence(evidence)
	prompt := BuildInstruction(evidenceBlock)

	call := &lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(prompt)}},
		},
	}

	req := auxiliary.Request{
		Role:                a.cfg.Role,
		Visibility:          Visibility,
		SessionMode:         auxiliary.SessionModeDetached,
		ParentTraceID:       evidence.ParentTraceID,
		ParentALegID:        evidence.ParentALegID,
		ParentBLegID:        evidence.ParentBLegID,
		ParentBranchBinding: evidence.ParentBranchBinding,
		DisablePlugins:      []string{PluginID},
		Call:                call,
	}
	// Fallback to context lineage if evidence fields are empty (backward compat / tests that rely on ctx).
	if req.ParentTraceID == "" {
		req.ParentTraceID = lineage.TraceID(ctx)
	}
	if req.ParentALegID == "" {
		req.ParentALegID = lineage.ALegID(ctx)
	}

	obs.ParentTraceID = req.ParentTraceID
	obs.ParentALegID = req.ParentALegID
	obs.ParentBLegID = req.ParentBLegID

	start := time.Now()
	collected, err := a.client.Collect(dctx, req)
	obs.Latency = time.Since(start)
	if err != nil {
		obs.Err = err
		return stopguard.Verdict{Kind: stopguard.VerdictUncertain}, err
	}
	obs.InputTokens = collected.InputTokens
	obs.OutputTokens = collected.OutputTokens
	obs.TotalTokens = collected.TotalTokens
	obs.CostNanoUnits = collected.CostNanoUnits
	text := strings.TrimSpace(collected.Text.String())
	if text == "" {
		perr := fmt.Errorf("verifier returned empty output")
		obs.Err = perr
		return stopguard.Verdict{Kind: stopguard.VerdictUncertain}, perr
	}
	v, perr := parseVerdict(text)
	if perr != nil {
		obs.Err = perr
		return stopguard.Verdict{Kind: stopguard.VerdictUncertain}, perr
	}
	// Apply conservative normalization and bounds already in parse; also run through
	// stopguard normalization to enforce CONTINUE objective rule.
	normalized := stopguard.NormalizeVerdict(v, nil)
	// If normalization downgraded to uncertain, preserve reason from original parse where useful.
	if normalized.Kind == stopguard.VerdictUncertain && v.Kind == stopguard.VerdictContinue {
		return normalized, nil
	}
	return normalized, nil
}

func parseVerdict(text string) (stopguard.Verdict, error) {
	// Allow surrounding whitespace, extract JSON object.
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < 0 || end < start {
		return stopguard.Verdict{}, fmt.Errorf("verifier output is not JSON object: %q", truncateString(text, 256))
	}
	raw := strings.TrimSpace(text[start : end+1])
	var payload struct {
		Kind               string `json:"kind"`
		Reason             string `json:"reason"`
		RemainingObjective string `json:"remaining_objective"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return stopguard.Verdict{}, fmt.Errorf("verifier JSON parse: %w", err)
	}
	kind := stopguard.VerdictKind(strings.TrimSpace(strings.ToLower(payload.Kind)))
	switch kind {
	case stopguard.VerdictAllowStop, stopguard.VerdictContinue, stopguard.VerdictNeedsUser, stopguard.VerdictBlocked, stopguard.VerdictUncertain:
	default:
		return stopguard.Verdict{Kind: stopguard.VerdictUncertain}, fmt.Errorf("unknown verdict kind %q", payload.Kind)
	}
	reason := boundText(payload.Reason, stopguard.MaxReasonBytes)
	objective := boundText(strings.TrimSpace(payload.RemainingObjective), stopguard.MaxRemainingObjectiveBytes)
	v := stopguard.Verdict{Kind: kind, Reason: reason, RemainingObjective: objective}
	if kind == stopguard.VerdictContinue && strings.TrimSpace(objective) == "" {
		return stopguard.Verdict{Kind: stopguard.VerdictUncertain}, fmt.Errorf("continue without remaining_objective")
	}
	return v, nil
}

func boundText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	return cut
}
