package routing

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidSelector reports a syntactically invalid route selector.
var ErrInvalidSelector = errors.New("routing: invalid route selector")

// Parse parses a route selector string into an AST.
func Parse(s string) (*Selector, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("%w: empty selector", ErrInvalidSelector)
	}
	globals, rest, err := extractGlobalParams(s)
	if err != nil {
		return nil, err
	}
	s = strings.TrimSpace(rest)
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
	return &Selector{Alternatives: alts, GlobalTTFTTimeout: globals.ttftTimeout}, nil
}

type globalParams struct {
	ttftTimeout *time.Duration
}

func extractGlobalParams(s string) (globalParams, string, error) {
	var out globalParams
	rest := strings.TrimSpace(s)
	if !strings.HasPrefix(rest, "{") {
		return out, rest, nil
	}
	idx := strings.Index(rest, "}")
	if idx < 0 {
		return globalParams{}, "", fmt.Errorf("%w: unclosed global parameter block", ErrInvalidSelector)
	}
	inside := strings.TrimSpace(rest[1:idx])
	if inside == "" {
		return globalParams{}, "", fmt.Errorf("%w: empty global parameter block", ErrInvalidSelector)
	}
	for entry := range strings.SplitSeq(inside, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return globalParams{}, "", fmt.Errorf("%w: malformed global parameter list", ErrInvalidSelector)
		}
		key, raw, hasValue := strings.Cut(entry, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if hasValue {
			raw = strings.TrimSpace(raw)
		}
		switch key {
		case "ttft_timeout":
			if out.ttftTimeout != nil {
				return globalParams{}, "", fmt.Errorf("%w: duplicate ttft_timeout global parameter", ErrInvalidSelector)
			}
			d, err := parsePositiveSecondsDurationAnnotation("ttft_timeout", raw, hasValue)
			if err != nil {
				return globalParams{}, "", err
			}
			out.ttftTimeout = &d
		default:
			return globalParams{}, "", fmt.Errorf("%w: unsupported global parameter key %q", ErrInvalidSelector, key)
		}
	}
	rest = strings.TrimSpace(rest[idx+1:])
	if strings.HasPrefix(rest, "{") {
		return globalParams{}, "", fmt.Errorf("%w: duplicate global parameter block", ErrInvalidSelector)
	}
	return out, rest, nil
}

func parseFailoverAlt(s string) (FailoverAlt, error) {
	s = strings.TrimSpace(s)
	// Weighted if '^' separates branches at depth 0, or the arm uses bracket annotations ([weight=], [first]).
	if hasTopLevelCaret(s) || hasWeightedAnnotationPrefix(s) {
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

func hasWeightedAnnotationPrefix(s string) bool {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "[") {
		idx := strings.Index(s, "]")
		if idx < 0 {
			return true
		}
		inside := s[1:idx]
		for entry := range strings.SplitSeq(inside, ",") {
			key := strings.TrimSpace(entry)
			if left, _, ok := strings.Cut(key, "="); ok {
				key = left
			}
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "weight", "first":
				return true
			}
		}
		s = strings.TrimSpace(s[idx+1:])
	}
	return false
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
		if b.IsFirst {
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
	ann, rest, err := extractPrefixAnnotations(s)
	if err != nil {
		return WeightedBranch{}, err
	}
	weight := 1
	if ann.weight != nil {
		weight = *ann.weight
	}
	first := ann.first
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return WeightedBranch{}, fmt.Errorf("%w: missing primary after annotations", ErrInvalidSelector)
	}
	p, err := parsePrimaryWithAnnotations(rest, ann)
	if err != nil {
		return WeightedBranch{}, err
	}
	return WeightedBranch{Weight: weight, IsFirst: first, Target: p}, nil
}

func parsePrimary(s string) (Primary, error) {
	ann, rest, err := extractPrefixAnnotations(s)
	if err != nil {
		return Primary{}, err
	}
	if ann.weight != nil || ann.first {
		return Primary{}, fmt.Errorf("%w: [weight] and [first] are only valid on weighted branches", ErrInvalidSelector)
	}
	return parsePrimaryWithAnnotations(rest, ann)
}

func parsePrimaryWithAnnotations(s string, ann prefixAnnotations) (Primary, error) {
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
	if containsGlobalParamBlock(path) {
		return Primary{}, fmt.Errorf("%w: global parameters must appear before the first leaf", ErrInvalidSelector)
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
	return Primary{Backend: backend, Model: model, Params: vals, Size: ann.size, TTFTTimeout: ann.ttftTimeout}, nil
}

func containsGlobalParamBlock(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' || (i > 0 && s[i-1] == '$') {
			continue
		}
		end := strings.IndexByte(s[i+1:], '}')
		if end < 0 {
			continue
		}
		inside := strings.TrimSpace(s[i+1 : i+1+end])
		key, _, _ := strings.Cut(inside, "=")
		if strings.EqualFold(strings.TrimSpace(key), "ttft_timeout") {
			return true
		}
		i += end + 1
	}
	return false
}

type prefixAnnotations struct {
	weight      *int
	first       bool
	size        RequestSizeConstraint
	ttftTimeout *time.Duration
}

func extractPrefixAnnotations(s string) (prefixAnnotations, string, error) {
	var ann prefixAnnotations
	rest := strings.TrimSpace(s)
	for strings.HasPrefix(rest, "[") {
		idx := strings.Index(rest, "]")
		if idx < 0 {
			return prefixAnnotations{}, "", fmt.Errorf("%w: unclosed annotation prefix", ErrInvalidSelector)
		}
		inside := strings.TrimSpace(rest[1:idx])
		if inside == "" {
			return prefixAnnotations{}, "", fmt.Errorf("%w: empty annotation prefix", ErrInvalidSelector)
		}
		for entry := range strings.SplitSeq(inside, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				return prefixAnnotations{}, "", fmt.Errorf("%w: malformed annotation list", ErrInvalidSelector)
			}
			key, raw, hasValue := strings.Cut(entry, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			if hasValue {
				raw = strings.TrimSpace(raw)
			}
			switch key {
			case "weight":
				if ann.weight != nil {
					return prefixAnnotations{}, "", fmt.Errorf("%w: duplicate [weight=N] annotation", ErrInvalidSelector)
				}
				n, err := parsePositiveIntAnnotation("weight", raw, hasValue)
				if err != nil {
					return prefixAnnotations{}, "", err
				}
				ann.weight = &n
			case "first":
				if ann.first {
					return prefixAnnotations{}, "", fmt.Errorf("%w: duplicate [first] annotation", ErrInvalidSelector)
				}
				if hasValue && strings.TrimSpace(raw) != "" {
					return prefixAnnotations{}, "", fmt.Errorf("%w: [first] does not take a value", ErrInvalidSelector)
				}
				ann.first = true
			case "max_context":
				if ann.size.MaxContextTokens != nil {
					return prefixAnnotations{}, "", fmt.Errorf("%w: duplicate [max_context=N] annotation", ErrInvalidSelector)
				}
				n, err := parsePositiveInt64Annotation("max_context", raw, hasValue)
				if err != nil {
					return prefixAnnotations{}, "", err
				}
				ann.size.MaxContextTokens = &n
			case "min_context":
				if ann.size.MinContextTokens != nil {
					return prefixAnnotations{}, "", fmt.Errorf("%w: duplicate [min_context=N] annotation", ErrInvalidSelector)
				}
				n, err := parsePositiveInt64Annotation("min_context", raw, hasValue)
				if err != nil {
					return prefixAnnotations{}, "", err
				}
				ann.size.MinContextTokens = &n
			case "ttft_timeout":
				if ann.ttftTimeout != nil {
					return prefixAnnotations{}, "", fmt.Errorf("%w: duplicate [ttft_timeout=N] annotation", ErrInvalidSelector)
				}
				d, err := parsePositiveSecondsDurationAnnotation("ttft_timeout", raw, hasValue)
				if err != nil {
					return prefixAnnotations{}, "", err
				}
				ann.ttftTimeout = &d
			default:
				return prefixAnnotations{}, "", fmt.Errorf("%w: unsupported annotation key %q", ErrInvalidSelector, key)
			}
		}
		rest = strings.TrimSpace(rest[idx+1:])
	}
	if ann.size.MinContextTokens != nil && ann.size.MaxContextTokens != nil && *ann.size.MinContextTokens >= *ann.size.MaxContextTokens {
		return prefixAnnotations{}, "", fmt.Errorf("%w: min_context must be less than max_context", ErrInvalidSelector)
	}
	return ann, rest, nil
}

func parsePositiveIntAnnotation(key, raw string, hasValue bool) (int, error) {
	n, err := parsePositiveInt64Annotation(key, raw, hasValue)
	if err != nil {
		return 0, err
	}
	if n > int64(int(^uint(0)>>1)) {
		return 0, fmt.Errorf("%w: %s is too large", ErrInvalidSelector, key)
	}
	return int(n), nil
}

func parsePositiveInt64Annotation(key, raw string, hasValue bool) (int64, error) {
	if !hasValue || strings.TrimSpace(raw) == "" {
		return 0, fmt.Errorf("%w: %s must be a positive integer", ErrInvalidSelector, key)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %s must be a positive integer", ErrInvalidSelector, key)
	}
	return n, nil
}

func parsePositiveSecondsDurationAnnotation(key, raw string, hasValue bool) (time.Duration, error) {
	n, err := parsePositiveInt64Annotation(key, raw, hasValue)
	if err != nil {
		return 0, err
	}
	if n > int64((1<<63-1)/time.Second) {
		return 0, fmt.Errorf("%w: %s is too large", ErrInvalidSelector, key)
	}
	return time.Duration(n) * time.Second, nil
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
