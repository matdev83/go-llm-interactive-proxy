package agentloopguard

import (
	"context"
	"errors"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/causepolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/progress"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/protocolstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/verifier"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

const providerID = "agent-loop-guard"

const (
	reasonCanceled          = "context_canceled"
	reasonDeadline          = "deadline_exceeded"
	reasonInvalidInput      = "invalid_input"
	reasonExplicitComplete  = "explicit_completion"
	reasonOutputUncommitted = "output_not_committed"
	reasonInsufficient      = "insufficient_evidence"
	reasonUnfinished        = "unfinished_objective"
	reasonBudgetExhausted   = "budget_exhausted"
	reasonInvalidProgress   = "invalid_progress_state"

	reasonProtocolInactive   = "completion_protocol_inactive"
	reasonMissingSignal      = "missing_completion_signal"
	reasonMissingSignalRetry = "missing_signal_reprompt"
	reasonRepromptExhausted  = "reprompt_exhausted"
	reasonInvalidProtocol    = "invalid_protocol_state"
)

const ProtocolRepairInstruction = `<automated-completion-protocol-repair>
The previous model turn ended without the required \`attempt_completion\` signal.
This is proxy-internal recovery control, not a new user request, approval,
permission, or scope expansion.

If all work requested by the user is complete, call \`attempt_completion\` now
with a concise final \`result\`.

If concrete requested work remains and can proceed without new user input,
continue exactly that work from the retained safe point.

If further progress requires user input, permission, credentials, clarification,
or a choice, request that input normally and end. Do not assume it.

Do not invent, repeat, broaden, optimize, or discover work merely because this
recovery message was sent.
</automated-completion-protocol-repair>`

// provider is the concrete ALG terminal policy. Strategy dispatch stays here;
// core sees only terminaldecision.Provider.
type provider struct{ cfg Config }

var _ terminaldecision.Provider = provider{}

// NewProvider constructs a stateless ALG provider. Omitted strategy preserves
// the historical semantic-verifier mode.
func NewProvider(config ...Config) terminaldecision.Provider {
	cfg, _ := (Config{Enabled: true}).Normalize()
	if len(config) > 0 {
		if normalized, err := config[0].Normalize(); err == nil {
			cfg = normalized
		}
	}
	return provider{cfg: cfg}
}

func (provider) ID() string { return providerID }

func (p provider) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	if p.cfg.Strategy == StrategyAttemptCompletion {
		return p.decideAttemptCompletion(ctx, in)
	}
	return p.decideSemanticVerifier(ctx, in)
}

// decideSemanticVerifier is the legacy ALG policy retained behaviorally intact.
func (p provider) decideSemanticVerifier(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return allowStop(reasonDeadline), nil
		}
		return allowStop(reasonCanceled), nil
	}
	if err := in.Validate(); err != nil {
		return allowStop(reasonInvalidInput), nil
	}
	causeInput := in
	if p.cfg.ExplicitCompletionPolicy == ExplicitCompletionPolicyVerify {
		causeInput.Evidence.ExplicitCompletion = false
	}
	causeResult := causepolicy.Evaluate(causeInput)
	if causeResult.Eligibility != causepolicy.EligibilityVerifier {
		return allowStop(string(causeResult.Reason)), nil
	}
	prior, ok := decodeProgressState(in)
	if !ok {
		return allowStop(reasonInvalidProgress), nil
	}

	projected := in
	projected.Policy.MaxContinuationAttempts = effectiveSemanticCap(p.cfg.MaxSemanticContinuations, in.Policy.MaxContinuationAttempts)
	verdict := progress.VerdictIncomplete
	if p.cfg.ExplicitCompletionPolicy != ExplicitCompletionPolicyTrust || !in.Evidence.ExplicitCompletion {
		semantic, err := verifier.New(in.Auxiliary, verifier.Config{
			Role:    p.cfg.VerifierRole,
			Timeout: p.cfg.VerifierTimeout,
		}).Verify(ctx, in)
		if err != nil || semantic.Kind == verifier.VerdictUncertain {
			return allowStop(progress.ReasonUncertain), nil
		}
		switch semantic.Kind {
		case verifier.VerdictComplete:
			if in.Evidence.ExplicitCompletion {
				return allowStop(reasonExplicitComplete), nil
			}
			return allowStop(progress.ReasonComplete), nil
		case verifier.VerdictIncomplete:
			if in.Evidence.ExplicitCompletion {
				projected.Evidence.ExplicitCompletion = false
			}
		default:
			return allowStop(progress.ReasonUncertain), nil
		}
	}
	if !hasConcreteUnfinishedWork(projected) {
		return allowStop(progress.ReasonMissingSafePoint), nil
	}
	evaluation, err := progress.Evaluate(projected, verdict, prior, progress.Config{NoProgressLimit: p.cfg.NoProgressLimit})
	if err != nil {
		return allowStop(progress.ReasonUncertain), nil
	}
	if evaluation.Action != progress.ActionContinue || evaluation.Decision.Continue == nil {
		return evaluation.Decision, nil
	}
	token, err := progress.EncodeState(evaluation.State)
	if err != nil {
		return allowStop(reasonInvalidProgress), nil
	}
	intent := *evaluation.Decision.Continue
	intent.ControlRef = token
	if err := intent.Validate(); err != nil {
		return allowStop(reasonInvalidProgress), nil
	}
	return terminaldecision.Decision{
		Kind:       terminaldecision.DecisionContinue,
		ReasonCode: progress.ReasonUnfinished,
		Continue:   &intent,
	}, nil
}

func (p provider) decideAttemptCompletion(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return allowStop(reasonDeadline), nil
		}
		return allowStop(reasonCanceled), nil
	}
	if err := in.Validate(); err != nil {
		return allowStop(reasonInvalidInput), nil
	}
	if in.Candidate.Cause.Authoritative() {
		return allowStop("authoritative_candidate"), nil
	}
	if in.Evidence.ExplicitCompletion {
		return allowStop(reasonExplicitComplete), nil
	}
	if in.Candidate.Cause == terminaldecision.CandidateCauseTransport && !in.Candidate.OutputCommitted {
		return allowStop("pre_output_transport"), nil
	}
	if !in.Candidate.OutputCommitted {
		return allowStop(reasonOutputUncommitted), nil
	}
	switch in.Candidate.Cause {
	case terminaldecision.CandidateCauseNormal, terminaldecision.CandidateCauseTransport,
		terminaldecision.CandidateCauseLimit, terminaldecision.CandidateCauseProviderError:
	default:
		return allowStop("unsupported_cause"), nil
	}
	if !safeProtocolActions(in) {
		return allowStop("unsafe_action"), nil
	}
	trajectory := strings.TrimSpace(in.Evidence.Lineage.TrajectoryRef)
	if trajectory == "" {
		trajectory = strings.TrimSpace(in.Continuation.TrajectoryRef)
	}
	if trajectory == "" {
		return allowStop("missing_trajectory"), nil
	}
	if !in.Evidence.ExplicitCompletionExpected {
		return allowStop(reasonProtocolInactive), nil
	}

	state, ok := decodeProtocolState(in)
	if !ok {
		return allowStop(reasonInvalidProtocol), nil
	}
	if state.Terminal {
		return allowStop(reasonRepromptExhausted), nil
	}
	cap := effectiveProtocolCap(p.cfg.MaxProtocolReprompts, in.Policy.MaxContinuationAttempts)
	if state.Reprompts >= cap {
		return allowStop(reasonRepromptExhausted), nil
	}

	fingerprint := protocolstate.Fingerprint(in)
	if state.LastFingerprint != "" {
		if state.LastFingerprint == fingerprint {
			state.ConsecutiveNoProgress++
		} else {
			state.ConsecutiveNoProgress = 0
		}
	}
	state.LastFingerprint = fingerprint
	if state.ConsecutiveNoProgress >= p.cfg.NoProgressLimit {
		return allowStop(progress.ReasonNoProgress), nil
	}

	state.Reprompts++
	token, err := protocolstate.Encode(state)
	if err != nil {
		return allowStop(reasonInvalidProtocol), nil
	}
	intent := terminaldecision.ContinuationIntent{
		TrajectoryRef: trajectory,
		ControlRef:    token,
		Instruction:   ProtocolRepairInstruction,
		Provenance:    "internal-control",
		ReasonCode:    reasonMissingSignal,
	}
	if err := intent.Validate(); err != nil {
		return allowStop(reasonInvalidProtocol), nil
	}
	return terminaldecision.Decision{
		Kind:       terminaldecision.DecisionContinue,
		ReasonCode: reasonMissingSignalRetry,
		Continue:   &intent,
	}, nil
}

func safeProtocolActions(in terminaldecision.Input) bool {
	count := min(int(in.Evidence.ActionCount), len(in.Evidence.Actions))
	for i := range count {
		action := in.Evidence.Actions[i]
		if (action.Kind == lipapi.ItemKindToolCall || action.Kind == lipapi.ItemKindToolResult) &&
			action.Status != lipapi.ItemStatusCompleted {
			return false
		}
	}
	return true
}

func effectiveProtocolCap(configCap int, platformCap uint8) int {
	if configCap <= 0 {
		configCap = DefaultMaxProtocolReprompts
	}
	if configCap > MaxProtocolReprompts {
		configCap = MaxProtocolReprompts
	}
	if platformCap > 0 && int(platformCap) < configCap {
		return int(platformCap)
	}
	return configCap
}

func decodeProtocolState(in terminaldecision.Input) (protocolstate.State, bool) {
	ref := strings.TrimSpace(in.Evidence.Lineage.ProgressRef)
	attempt := max(in.Continuation.Attempt, in.Evidence.Lineage.Attempt)
	if ref == "" {
		return protocolstate.State{}, attempt <= 1
	}
	if attempt <= 1 && !strings.HasPrefix(ref, protocolstate.Prefix) {
		return protocolstate.State{}, true
	}
	state, err := protocolstate.Decode(ref)
	if err != nil {
		return protocolstate.State{}, false
	}
	return state, true
}

// hasConcreteUnfinishedWork is the final legacy provider-local safe-point check.
func hasConcreteUnfinishedWork(in terminaldecision.Input) bool {
	count := min(int(in.Evidence.ActionCount), len(in.Evidence.Actions))
	for i := range count {
		action := in.Evidence.Actions[i]
		if action.Kind == lipapi.ItemKindMessage && action.Status == lipapi.ItemStatusInProgress {
			return true
		}
	}
	return false
}

func effectiveSemanticCap(configCap int, platformCap uint8) uint8 {
	if configCap <= 0 {
		configCap = DefaultMaxSemanticContinuations
	}
	if configCap > 255 {
		configCap = 255
	}
	cap := uint8(configCap)
	if platformCap > 0 && platformCap < cap {
		return platformCap
	}
	return cap
}

func decodeProgressState(in terminaldecision.Input) (progress.State, bool) {
	ref := strings.TrimSpace(in.Evidence.Lineage.ProgressRef)
	attempt := max(in.Continuation.Attempt, in.Evidence.Lineage.Attempt)
	if ref == "" {
		return progress.State{}, attempt <= 1
	}
	if attempt <= 1 && !strings.HasPrefix(ref, "alg-state-v1.") {
		return progress.State{}, true
	}
	state, err := progress.DecodeState(ref)
	if err != nil {
		return progress.State{}, false
	}
	return state, true
}

func allowStop(reason string) terminaldecision.Decision {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: reason}
}
