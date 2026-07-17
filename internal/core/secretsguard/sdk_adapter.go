package secretsguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type matcherAdapter struct {
	m *Matcher
}

// AsMatcher adapts *Matcher to secretguard.Matcher.
func AsMatcher(m *Matcher) secretguard.Matcher {
	if m == nil {
		m = &Matcher{}
	}
	return matcherAdapter{m: m}
}

func (a matcherAdapter) ScanBytes(ctx context.Context, input []byte) ([]secretguard.Finding, error) {
	_ = ctx
	return a.m.ScanBytes(input), nil
}

func (a matcherAdapter) ScanString(ctx context.Context, input string) ([]secretguard.Finding, error) {
	_ = ctx
	return a.m.ScanString(input), nil
}

func (a matcherAdapter) RedactBytes(ctx context.Context, input []byte) ([]byte, []secretguard.Finding, error) {
	_ = ctx
	out, findings := a.m.RedactBytes(input)
	return out, findings, nil
}

func (a matcherAdapter) RedactString(ctx context.Context, input string) (string, []secretguard.Finding, error) {
	_ = ctx
	out, findings := a.m.RedactString(input)
	return out, findings, nil
}

type staticMatcherResolver struct {
	m secretguard.Matcher
}

// NewStaticMatcherResolver returns a MatcherResolver that always resolves to a matcher for cat.
// Callers pass MatcherOptions; composition typically enables PreserveKnownPrefixes.
func NewStaticMatcherResolver(cat *Catalog, opts MatcherOptions) secretguard.MatcherResolver {
	return staticMatcherResolver{m: AsMatcher(NewMatcherWithOptions(cat, opts))}
}

func (r staticMatcherResolver) Resolve(ctx context.Context) (secretguard.Matcher, error) {
	_ = ctx
	return r.m, nil
}

var (
	_ secretguard.Matcher         = matcherAdapter{}
	_ secretguard.MatcherResolver = staticMatcherResolver{}
)
