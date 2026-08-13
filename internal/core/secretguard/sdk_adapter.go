package secretguard

import (
	"context"

	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type matcherAdapter struct {
	m *Matcher
}

// AsMatcher adapts *Matcher to sdk.Matcher.
func AsMatcher(m *Matcher) sdk.Matcher {
	if m == nil {
		m = &Matcher{}
	}
	return matcherAdapter{m: m}
}

func (a matcherAdapter) ScanBytes(ctx context.Context, input []byte) ([]sdk.Finding, error) {
	_ = ctx
	return a.m.ScanBytes(input), nil
}

func (a matcherAdapter) ScanString(ctx context.Context, input string) ([]sdk.Finding, error) {
	_ = ctx
	return a.m.ScanString(input), nil
}

func (a matcherAdapter) RedactBytes(ctx context.Context, input []byte) ([]byte, []sdk.Finding, error) {
	_ = ctx
	out, findings := a.m.RedactBytes(input)
	return out, findings, nil
}

func (a matcherAdapter) RedactString(ctx context.Context, input string) (string, []sdk.Finding, error) {
	_ = ctx
	out, findings := a.m.RedactString(input)
	return out, findings, nil
}

type staticMatcherResolver struct {
	m sdk.Matcher
}

// NewStaticMatcherResolver returns a MatcherResolver that always resolves to a matcher for cat.
// Callers pass MatcherOptions; composition typically enables PreserveKnownPrefixes.
func NewStaticMatcherResolver(cat *Catalog, opts MatcherOptions) sdk.MatcherResolver {
	return staticMatcherResolver{m: AsMatcher(NewMatcherWithOptions(cat, opts))}
}

func (r staticMatcherResolver) Resolve(ctx context.Context) (sdk.Matcher, error) {
	_ = ctx
	return r.m, nil
}

var (
	_ sdk.Matcher         = matcherAdapter{}
	_ sdk.MatcherResolver = staticMatcherResolver{}
)
