package secretguard

// ExecutionConfig carries generation-composed secret-guard execution
// configuration bound by the standard distribution into the ordinary
// extension plane set. Guards themselves travel on PlaneSecretGuards; this
// struct carries the engine-composed matcher, audit, policy, and diagnostics
// posture that generic runtime execution needs alongside them.
type ExecutionConfig struct {
	MatcherResolver    MatcherResolver
	DecisionObserver   Observer
	AuditFailurePolicy AuditFailurePolicy
	AccessMode         string
	ConfigVersion      string
	// CatalogEntryCount, SourceCategories, and CatalogAction mirror the
	// operator diagnostics inventory for the composed static catalog.
	CatalogEntryCount int
	SourceCategories  []string
	CatalogAction     string
}

// CloneExecutionConfig deep-copies an execution configuration for frozen-set
// isolation: the container struct and the categories slice are copied, while
// the shared service capabilities (MatcherResolver, DecisionObserver) are
// intentionally preserved by reference — they are immutable engine handles,
// not configuration. A nil input yields nil, preserving NilSkip semantics.
func CloneExecutionConfig(in *ExecutionConfig) *ExecutionConfig {
	if in == nil {
		return nil
	}
	out := *in
	out.SourceCategories = append([]string(nil), in.SourceCategories...)
	return &out
}

// IsZero reports whether c carries no composed execution configuration
// (secret guard disabled for the generation).
func (c ExecutionConfig) IsZero() bool {
	return c.MatcherResolver == nil &&
		IsNilObserver(c.DecisionObserver) &&
		c.AuditFailurePolicy == "" &&
		c.AccessMode == "" &&
		c.ConfigVersion == "" &&
		c.CatalogEntryCount == 0 &&
		len(c.SourceCategories) == 0 &&
		c.CatalogAction == ""
}
