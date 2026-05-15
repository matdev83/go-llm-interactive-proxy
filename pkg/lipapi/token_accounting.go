package lipapi

// UsagePlane identifies the accounting perspective for a usage delta.
type UsagePlane string

const (
	// UsagePlaneUnknown is the zero value for absent or unspecified usage plane metadata.
	UsagePlaneUnknown UsagePlane = ""
	// UsagePlaneProviderBillable is the provider-reported or provider-derived billable plane.
	UsagePlaneProviderBillable UsagePlane = "provider_billable"
	// UsagePlaneClientVisible is the usage visible to the upstream client protocol.
	UsagePlaneClientVisible UsagePlane = "client_visible"
	// UsagePlaneProxyBillable is usage billed by proxy policy rather than directly by a provider.
	UsagePlaneProxyBillable UsagePlane = "proxy_billable"
)

// UsageSource identifies where usage accounting metadata came from.
type UsageSource string

const (
	// UsageSourceUnknown is the zero value for absent or unspecified usage source metadata.
	UsageSourceUnknown          UsageSource = ""
	UsageSourceProviderReported UsageSource = "provider_reported"
	UsageSourceProviderCountAPI UsageSource = "provider_count_api"
	UsageSourceLocalTokenizer   UsageSource = "local_tokenizer"
	UsageSourceLocalEstimator   UsageSource = "local_estimator"
	UsageSourcePolicyReserved   UsageSource = "policy_reserved"
	UsageSourceProxyAdjusted    UsageSource = "proxy_adjusted"
	UsageSourceUnavailable      UsageSource = "unavailable"
)

// UsageAuthority describes how strongly callers may rely on a usage delta.
type UsageAuthority string

const (
	// UsageAuthorityUnknown is the zero value for absent or unspecified authority metadata.
	UsageAuthorityUnknown       UsageAuthority = ""
	UsageAuthorityAuthoritative UsageAuthority = "authoritative"
	UsageAuthorityDelegated     UsageAuthority = "delegated"
	UsageAuthorityEstimated     UsageAuthority = "estimated"
	UsageAuthorityAdvisory      UsageAuthority = "advisory"
	UsageAuthorityUnavailable   UsageAuthority = "unavailable"
)

// TokenizerRef records provider-neutral tokenizer metadata used to derive usage.
type TokenizerRef struct {
	Type      string
	ID        string
	Version   string
	Source    string
	ModelUsed string
}

// UsageAccountingMetadata annotates usage deltas without replacing legacy token totals.
type UsageAccountingMetadata struct {
	Plane     UsagePlane
	Source    UsageSource
	Authority UsageAuthority
	Tokenizer TokenizerRef
}

// ScopedUsageDelta carries usage for one accounting plane while preserving Event legacy totals.
type ScopedUsageDelta struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	TotalTokens      int
	Accounting       UsageAccountingMetadata
}

func (m UsageAccountingMetadata) validate(field string) error {
	if err := validateStringField(field+".Plane", string(m.Plane), MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".Source", string(m.Source), MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".Authority", string(m.Authority), MaxRefStringBytes); err != nil {
		return err
	}
	return m.Tokenizer.validate(field + ".Tokenizer")
}

func (r TokenizerRef) validate(field string) error {
	if err := validateStringField(field+".Type", r.Type, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".ID", r.ID, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".Version", r.Version, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".Source", r.Source, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".ModelUsed", r.ModelUsed, MaxRefStringBytes); err != nil {
		return err
	}
	return nil
}

func validateScopedUsage(scopes []ScopedUsageDelta) error {
	for i, scope := range scopes {
		if err := scope.Accounting.validate(fieldIndex("UsageScopes", i) + ".Accounting"); err != nil {
			return err
		}
	}
	return nil
}

func cloneEvent(ev Event) Event {
	ev.UsageScopes = append([]ScopedUsageDelta(nil), ev.UsageScopes...)
	return ev
}
