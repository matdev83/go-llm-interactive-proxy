package routing

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ErrInvalidSelector reports a syntactically invalid route selector.
var ErrInvalidSelector = errors.New("routing: invalid route selector")

// Parse parses a route selector string into an AST.
func Parse(s string) (*Selector, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("%w: empty selector", ErrInvalidSelector)
	}
	parts := splitOutsideBrackets(s, '|')
	alts := make([]FailoverAlt, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("%w: empty failover alternative", ErrInvalidSelector)
		}
		alt, err := parseFailoverAlt(part)
		if err != nil {
			return nil, err
		}
		alts = append(alts, alt)
	}
	return &Selector{Alternatives: alts}, nil
}

func parseFailoverAlt(s string) (FailoverAlt, error) {
	s = strings.TrimSpace(s)
	// Weighted if '^' separates branches at depth 0, or the arm uses bracket annotations ([weight=], [first]).
	if hasTopLevelCaret(s) || strings.HasPrefix(s, "[") {
		w, err := parseWeighted(s)
		if err != nil {
			return FailoverAlt{}, err
		}
		if err := validateWeightedFirst(w); err != nil {
			return FailoverAlt{}, err
		}
		return FailoverAlt{Weighted: w}, nil
	}
	p, err := parsePrimary(s)
	if err != nil {
		return FailoverAlt{}, err
	}
	return FailoverAlt{Primary: &p}, nil
}

func hasTopLevelCaret(s string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case '^':
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

func validateWeightedFirst(w *Weighted) error {
	if w == nil {
		return nil
	}
	n := 0
	for _, b := range w.Branches {
		if b.First {
			n++
		}
	}
	if n > 1 {
		return fmt.Errorf("%w: at most one [first] branch is allowed in a weighted selector", ErrInvalidSelector)
	}
	return nil
}

func parseWeighted(s string) (*Weighted, error) {
	parts := splitOutsideBrackets(s, '^')
	branches := make([]WeightedBranch, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("%w: empty weighted branch", ErrInvalidSelector)
		}
		b, err := parseWeightedBranch(part)
		if err != nil {
			return nil, err
		}
		branches = append(branches, b)
	}
	if len(branches) == 0 {
		return nil, fmt.Errorf("%w: no weighted branches", ErrInvalidSelector)
	}
	return &Weighted{Branches: branches}, nil
}

func parseWeightedBranch(s string) (WeightedBranch, error) {
	weight := 1
	first := false
	rest := strings.TrimSpace(s)
	for {
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, "[weight=") {
			idx := strings.Index(rest, "]")
			if idx < 0 {
				return WeightedBranch{}, fmt.Errorf("%w: unclosed [weight=...]", ErrInvalidSelector)
			}
			inside := rest[len("[weight="):idx]
			inside = strings.TrimSpace(inside)
			n, err := strconv.Atoi(inside)
			if err != nil || n <= 0 {
				return WeightedBranch{}, fmt.Errorf("%w: weight must be a positive integer", ErrInvalidSelector)
			}
			weight = n
			rest = rest[idx+1:]
			continue
		}
		if strings.HasPrefix(rest, "[first]") {
			first = true
			rest = rest[len("[first]"):]
			continue
		}
		break
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return WeightedBranch{}, fmt.Errorf("%w: missing primary after annotations", ErrInvalidSelector)
	}
	p, err := parsePrimary(rest)
	if err != nil {
		return WeightedBranch{}, err
	}
	return WeightedBranch{Weight: weight, First: first, Target: p}, nil
}

func parsePrimary(s string) (Primary, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Primary{}, fmt.Errorf("%w: empty primary", ErrInvalidSelector)
	}
	path, queryStr, hasQuery := strings.Cut(raw, "?")
	var vals url.Values
	if hasQuery {
		q, err := url.ParseQuery(queryStr)
		if err != nil {
			return Primary{}, fmt.Errorf("%w: parse query: %w", ErrInvalidSelector, err)
		}
		vals = q
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return Primary{}, fmt.Errorf("%w: missing model in primary", ErrInvalidSelector)
	}
	backend, model, hasColon := strings.Cut(path, ":")
	if !hasColon {
		backend = ""
		model = path
	}
	backend = strings.TrimSpace(backend)
	model = strings.TrimSpace(model)
	if model == "" {
		return Primary{}, fmt.Errorf("%w: model is required", ErrInvalidSelector)
	}
	if strings.Contains(backend, "|") || strings.Contains(model, "|") {
		return Primary{}, fmt.Errorf("%w: unexpected '|' in primary (use failover '|' at top level)", ErrInvalidSelector)
	}
	return Primary{Backend: backend, Model: model, Params: vals}, nil
}

func splitOutsideBrackets(s string, sep byte) []string {
	out := []string{}
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case sep:
			if depth == 0 && s[i] == sep {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}
