package auth

import (
	"bytes"
	"cmp"
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// exactCredentialMatcher holds presented request credential bytes privately and matches
// exact occurrences for secret-guard scanning/redaction.
type exactCredentialMatcher struct {
	secret  []byte
	refName string
}

func newExactCredentialMatcher(presented, keyID string) *exactCredentialMatcher {
	presented = strings.TrimSpace(presented)
	if presented == "" {
		return nil
	}
	ref := cmp.Or(strings.TrimSpace(keyID), "request_credential")
	sec := make([]byte, len(presented))
	copy(sec, presented)
	return &exactCredentialMatcher{secret: sec, refName: ref}
}

func (m *exactCredentialMatcher) ScanBytes(ctx context.Context, input []byte) ([]secretguard.Finding, error) {
	_ = ctx
	if m == nil || len(m.secret) == 0 {
		return nil, nil
	}
	n := countExactOccurrences(input, m.secret)
	if n == 0 {
		return nil, nil
	}
	return []secretguard.Finding{{
		SecretRefName:   m.refName,
		SourceCategory:  secretguard.SourceCategoryRequestCred,
		OccurrenceCount: n,
	}}, nil
}

func (m *exactCredentialMatcher) ScanString(ctx context.Context, input string) ([]secretguard.Finding, error) {
	return m.ScanBytes(ctx, []byte(input))
}

func (m *exactCredentialMatcher) RedactBytes(ctx context.Context, input []byte) ([]byte, []secretguard.Finding, error) {
	_ = ctx
	if m == nil || len(m.secret) == 0 || len(input) == 0 {
		return append([]byte(nil), input...), nil, nil
	}
	n := countExactOccurrences(input, m.secret)
	if n == 0 {
		return append([]byte(nil), input...), nil, nil
	}
	mask := bytes.Repeat([]byte("*"), len(m.secret))
	out := bytes.ReplaceAll(input, m.secret, mask)
	return out, []secretguard.Finding{{
		SecretRefName:   m.refName,
		SourceCategory:  secretguard.SourceCategoryRequestCred,
		OccurrenceCount: n,
	}}, nil
}

func (m *exactCredentialMatcher) RedactString(ctx context.Context, input string) (string, []secretguard.Finding, error) {
	out, findings, err := m.RedactBytes(ctx, []byte(input))
	if err != nil {
		return "", nil, err
	}
	return string(out), findings, nil
}

func countExactOccurrences(haystack, needle []byte) int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return 0
	}
	n := 0
	for i := 0; ; {
		j := bytes.Index(haystack[i:], needle)
		if j < 0 {
			return n
		}
		n++
		i += j + len(needle)
		if i > len(haystack) {
			return n
		}
	}
}

var _ secretguard.Matcher = (*exactCredentialMatcher)(nil)
