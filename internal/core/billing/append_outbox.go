package billing

// UsageAppendKind and UsageAppendWork are immutable migration records used by
// the explicit Phase-2 legacy outbox drain. They are not runtime delivery
// ports; terminal traffic uses TerminalUsageSink.
type UsageAppendKind string

const (
	UsageAppendCall UsageAppendKind = "call"
	UsageAppendLeg  UsageAppendKind = "leg"
)

type UsageAppendWork struct {
	Key  string
	Kind UsageAppendKind
	Call *CallUsageRecord
	Leg  *CallLegUsageRecord
}
