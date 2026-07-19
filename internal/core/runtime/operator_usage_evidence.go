package runtime

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

// emptyOperatorUsageShell is the legal unobserved operator usage event for
// incurred settle/egress when no provider usage was seen (req 1.5 / 2.9).
func emptyOperatorUsageShell() lipapi.Event {
	return lipapi.Event{Kind: lipapi.EventUsageDelta}
}

// operatorUsageEvidence derives authority-preferring operator usage from observed
// stream events. Returns an empty Kind when no UsageDelta was present.
func operatorUsageEvidence(events []lipapi.Event) lipapi.Event {
	return authorityUsageEvent(tokenAccountingUsageEvents(events))
}

// operatorUsageOrShell returns observed evidence or the empty shell.
func operatorUsageOrShell(events []lipapi.Event) lipapi.Event {
	if ev := operatorUsageEvidence(events); ev.Kind != "" {
		return ev
	}
	return emptyOperatorUsageShell()
}

// operatorUsageForFinalize prefers reconstructed authority usage, then seen
// stream evidence, then the empty shell for unobserved incurred terminals.
func (s *retryRecvStream) operatorUsageForFinalize() lipapi.Event {
	if s == nil {
		return emptyOperatorUsageShell()
	}
	if s.lastAuthorityUsage.Kind != "" {
		return s.lastAuthorityUsage
	}
	return operatorUsageOrShell(s.seenEvents)
}
