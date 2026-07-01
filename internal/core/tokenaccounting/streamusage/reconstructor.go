// Package streamusage reconstructs scoped usage for completed streaming calls.
package streamusage

import (
	"context"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type Counter interface {
	CountCall(context.Context, app.CountCallInput) (app.CountResult, error)
	CountOutput(context.Context, app.CountOutputInput) (app.CountResult, error)
}

type Config struct {
	Policy        domain.BillingPolicy
	ProxyBillable bool
}

type Reconstructor struct {
	counter Counter
	policy  domain.BillingPolicy
	proxy   bool
}

type Input struct {
	Backend    string
	Model      string
	Call       lipapi.Call
	OutputText string
	Events     []lipapi.Event
}

type WarningCode string

const (
	WarningInputCountUnavailable  WarningCode = "input_count_unavailable"
	WarningOutputCountUnavailable WarningCode = "output_count_unavailable"
	WarningContextCanceled        WarningCode = "context_canceled"
)

type Warning struct {
	Code    WarningCode
	Message string
	Err     error
}

type Result struct {
	Events     []lipapi.Event
	Reconciled domain.Result
	Warnings   []Warning
	Err        error
}

func New(counter Counter, cfg Config) *Reconstructor {
	policy := cfg.Policy
	if policy == "" {
		policy = domain.PolicyProviderThenClient
	}
	return &Reconstructor{counter: counter, policy: policy, proxy: cfg.ProxyBillable}
}

func (r *Reconstructor) Reconstruct(ctx context.Context, input Input) (Result, error) {
	result := Result{}
	providerEvents := usageEvents(input.Events)
	result.Events = append(result.Events, providerEvents...)

	if err := ctx.Err(); err != nil {
		result.Warnings = append(result.Warnings, Warning{Code: WarningContextCanceled, Message: "stream usage reconstruction canceled", Err: err})
		result.Reconciled = domain.ReconcileEvents(r.policy, result.Events...)
		result.Err = err
		return result, err
	}

	local, warnings, err := r.localUsage(ctx, input)
	result.Warnings = append(result.Warnings, warnings...)
	if local != nil {
		result.Events = append(result.Events, usageEvent(*local))
		if r.proxy {
			proxy := *local
			proxy.Accounting.Plane = lipapi.UsagePlaneProxyBillable
			proxy.Accounting.Source = lipapi.UsageSourceProxyAdjusted
			result.Events = append(result.Events, usageEvent(proxy))
		}
	}

	result.Reconciled = domain.ReconcileEvents(r.policy, result.Events...)
	result.Err = err
	return result, err
}

func (r *Reconstructor) localUsage(ctx context.Context, input Input) (*lipapi.ScopedUsageDelta, []Warning, error) {
	if r.counter == nil {
		err := errors.New("stream usage counter is nil")
		return nil, []Warning{{Code: WarningInputCountUnavailable, Message: "input token count unavailable", Err: err}}, err
	}

	callResult, err := r.counter.CountCall(ctx, app.CountCallInput{
		Backend: input.Backend,
		Model:   input.Model,
		CallID:  input.Call.ID,
		Call:    input.Call,
	})
	if err != nil {
		return nil, []Warning{warningForCountError(WarningInputCountUnavailable, "input token count unavailable", err)}, err
	}

	outputResult, err := r.counter.CountOutput(ctx, app.CountOutputInput{
		Backend: input.Backend,
		Model:   input.Model,
		CallID:  input.Call.ID,
		Text:    input.OutputText,
		Events:  nonUsageEvents(input.Events),
	})
	if err != nil {
		usage := localUsageFromResults(callResult, app.CountResult{})
		return &usage, []Warning{warningForCountError(WarningOutputCountUnavailable, "output token count unavailable", err)}, err
	}

	usage := localUsageFromResults(callResult, outputResult)
	return &usage, nil, nil
}

func usageEvents(events []lipapi.Event) []lipapi.Event {
	out := []lipapi.Event{}
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			out = append(out, normalizeProviderUsageEvent(ev))
		}
	}
	return out
}

func normalizeProviderUsageEvent(ev lipapi.Event) lipapi.Event {
	out := cloneEvent(ev)
	if len(out.UsageScopes) == 0 {
		out.Accounting = defaultProviderAccounting(out.Accounting)
		return out
	}
	for i := range out.UsageScopes {
		out.UsageScopes[i].Accounting = defaultProviderAccounting(out.UsageScopes[i].Accounting)
	}
	return out
}

func defaultProviderAccounting(accounting lipapi.UsageAccountingMetadata) lipapi.UsageAccountingMetadata {
	if accounting.Plane == lipapi.UsagePlaneUnknown {
		accounting.Plane = lipapi.UsagePlaneProviderBillable
	}
	if accounting.Source == lipapi.UsageSourceUnknown {
		accounting.Source = lipapi.UsageSourceProviderReported
	}
	if accounting.Authority == lipapi.UsageAuthorityUnknown {
		accounting.Authority = lipapi.UsageAuthorityAuthoritative
	}
	return accounting
}

func localUsageFromResults(callResult, outputResult app.CountResult) lipapi.ScopedUsageDelta {
	usage := lipapi.ScopedUsageDelta{
		InputTokens:      callResult.InputTokens,
		OutputTokens:     outputResult.OutputTokens,
		CacheReadTokens:  callResult.CacheReadTokens + outputResult.CacheReadTokens,
		CacheWriteTokens: callResult.CacheWriteTokens + outputResult.CacheWriteTokens,
		ReasoningTokens:  outputResult.ReasoningTokens,
		Accounting:       mergeAccounting(callResult.Accounting, outputResult.Accounting, lipapi.UsagePlaneClientVisible),
	}
	usage.TotalTokens = totalTokens(usage, callResult.TotalTokens, outputResult.TotalTokens)
	return usage
}

func usageEvent(usage lipapi.ScopedUsageDelta) lipapi.Event {
	return lipapi.Event{
		Kind:             lipapi.EventUsageDelta,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		TotalTokens:      usage.TotalTokens,
		Accounting:       usage.Accounting,
		UsageScopes:      []lipapi.ScopedUsageDelta{usage},
	}
}

func mergeAccounting(input, output lipapi.UsageAccountingMetadata, plane lipapi.UsagePlane) lipapi.UsageAccountingMetadata {
	accounting := output
	if accounting.Source == lipapi.UsageSourceUnknown {
		accounting.Source = input.Source
	}
	if accounting.Authority == lipapi.UsageAuthorityUnknown {
		accounting.Authority = input.Authority
	}
	if accounting.Tokenizer == (lipapi.TokenizerRef{}) {
		accounting.Tokenizer = input.Tokenizer
	}
	accounting.Plane = plane
	if accounting.Source == lipapi.UsageSourceUnknown {
		accounting.Source = lipapi.UsageSourceLocalEstimator
	}
	if accounting.Authority == lipapi.UsageAuthorityUnknown {
		accounting.Authority = lipapi.UsageAuthorityEstimated
	}
	return accounting
}

func totalTokens(usage lipapi.ScopedUsageDelta, inputTotal, outputTotal int) int {
	if inputTotal > 0 || outputTotal > 0 {
		return inputTotal + outputTotal
	}
	return usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheWriteTokens + usage.ReasoningTokens
}

func warningForCountError(code WarningCode, message string, err error) Warning {
	if errors.Is(err, context.Canceled) {
		return Warning{Code: WarningContextCanceled, Message: "stream usage reconstruction canceled", Err: err}
	}
	return Warning{Code: code, Message: fmt.Sprintf("%s: %v", message, err), Err: err}
}

func nonUsageEvents(events []lipapi.Event) []lipapi.Event {
	out := []lipapi.Event{}
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			continue
		}
		out = append(out, cloneEvent(ev))
	}
	return out
}

func cloneEvent(ev lipapi.Event) lipapi.Event {
	ev.UsageScopes = append([]lipapi.ScopedUsageDelta(nil), ev.UsageScopes...)
	return ev
}
