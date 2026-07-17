package reasoningpreservation

const BuiltinCatalogVersion = "kimi-moonshot.v1"

type CandidateIdentity struct {
	BackendID       string
	BackendPrefixes []string
	Model           string
}

type MatchKind string

const (
	MatchNone                    MatchKind = "none"
	MatchExplicitDisabledModel   MatchKind = "explicit_disabled_model"
	MatchExplicitEnabledModel    MatchKind = "explicit_enabled_model"
	MatchExplicitDisabledBackend MatchKind = "explicit_disabled_backend"
	MatchExplicitEnabledBackend  MatchKind = "explicit_enabled_backend"
	MatchBuiltin                 MatchKind = "builtin"
)

type MatchResult struct {
	Kind   MatchKind
	RuleID string
}

type BuiltinCatalogEntry struct {
	ID              string
	BackendPrefixes []string
	ModelKeywords   []string
}

func ResolveMatch(cfg Config, cand CandidateIdentity) (MatchResult, error) {
	_ = cfg
	_ = cand
	return MatchResult{}, ErrNotImplemented
}

func BuiltinCatalogEntries() ([]BuiltinCatalogEntry, error) {
	return nil, ErrNotImplemented
}
