package secretguard

import (
	"bytes"
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard/engine"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type scanMode int

const (
	modeScan scanMode = iota
	modeRedact
)

type scanOutcome struct {
	Findings             []sdk.Finding
	MutationCount        int
	BytesScanned         int
	ScanLimitHit         bool
	exactPrivateFindings []exactPrivateFinding
}

type exactOccurrenceMatcher interface {
	ScanOccurrences(input []byte) []engine.Occurrence
}

// collectExactPrivateFindings retains exact spans only inside the feature
// boundary. The matcher receives the admitted fragment bytes and returns safe
// reference metadata; it has no catalog retrieval surface.
func collectExactPrivateFindings(m sdk.Matcher, fragment LogicalFragment, findings []sdk.Finding) []exactPrivateFinding {
	if m == nil || len(findings) == 0 {
		return nil
	}
	positional, ok := m.(exactOccurrenceMatcher)
	if !ok {
		return privateExactFindingsWithoutSpans(fragment, findings)
	}
	private := make([]betterLeaksOccurrence, 0)
	if fragment.Kind == FragmentJSON {
		// A successfully decoded JSON fragment is collected from the same
		// semantic tokens as scanJSONPayload. Raw scanning is reserved for the
		// decoder-failed fallback so escaped bytes and punctuation cannot add
		// independent occurrences.
		var mapped bool
		private, mapped = collectExactJSONOccurrencesMapped(positional, fragment.Raw, fragment.privateID)
		if !mapped {
			private = collectExactRawOccurrences(positional, fragment.Raw, fragment.privateID)
		}
	} else {
		private = collectExactRawOccurrences(positional, fragment.Raw, fragment.privateID)
	}
	return exactPrivateFindingsAt(fragment, findings, private)
}

func collectExactPrivateFindingsForRedact(m sdk.Matcher, fragment LogicalFragment, findings []sdk.Finding) []exactPrivateFinding {
	if m == nil || len(findings) == 0 {
		return nil
	}
	positional, ok := m.(exactOccurrenceMatcher)
	if !ok {
		return privateExactFindingsWithoutSpans(fragment, findings)
	}
	var private []betterLeaksOccurrence
	if fragment.Kind == FragmentJSON {
		var mapped bool
		private, mapped = collectExactJSONRedactOccurrences(positional, fragment.Raw, fragment.privateID)
		if !mapped {
			private = collectExactRawOccurrences(positional, fragment.Raw, fragment.privateID)
		}
	} else {
		private = collectExactRawOccurrences(positional, fragment.Raw, fragment.privateID)
	}
	return exactPrivateFindingsAt(fragment, findings, private)
}

func exactPrivateFindingsAt(fragment LogicalFragment, findings []sdk.Finding, private []betterLeaksOccurrence) []exactPrivateFinding {
	result := make([]exactPrivateFinding, 0, len(findings))
	for _, finding := range findings {
		finding.Location = fragment.Location
		matched := make([]betterLeaksOccurrence, 0, len(private))
		for _, occurrence := range private {
			if occurrence.ruleID == finding.SecretRefName {
				matched = append(matched, occurrence)
			}
		}
		uncovered := finding.OccurrenceCount - len(matched)
		if uncovered < 0 {
			uncovered = 0
		}
		result = append(result, exactPrivateFinding{finding: finding, occurrences: matched, uncoveredCount: uncovered})
	}
	return result
}

func collectExactRawOccurrences(m exactOccurrenceMatcher, raw []byte, fieldID string) []betterLeaksOccurrence {
	occurrences := m.ScanOccurrences(raw)
	private := make([]betterLeaksOccurrence, 0, len(occurrences))
	for _, occurrence := range occurrences {
		if occurrence.Start < 0 || occurrence.End <= occurrence.Start || occurrence.End > len(raw) {
			continue
		}
		span, err := spanForByteRange(raw, occurrence.Start, occurrence.End)
		if err != nil {
			continue
		}
		private = append(private, betterLeaksOccurrence{
			value:          bytes.Clone(raw[occurrence.Start:occurrence.End]),
			span:           span,
			fieldID:        fieldID,
			ruleID:         occurrence.SecretRefName,
			role:           betterLeaksOccurrencePrimary,
			representation: betterLeaksOccurrenceLiteral,
		})
	}
	return private
}

func privateExactFindingsWithoutSpans(fragment LogicalFragment, findings []sdk.Finding) []exactPrivateFinding {
	result := make([]exactPrivateFinding, 0, len(findings))
	for _, finding := range findings {
		finding.Location = fragment.Location
		result = append(result, exactPrivateFinding{finding: finding, uncoveredCount: positiveOccurrenceCount(finding.OccurrenceCount)})
	}
	return result
}

// scanCall scans the one admitted logical-fragment set. Locations are stable paths.
func scanCall(ctx context.Context, call *lipapi.Call, m sdk.Matcher, mode scanMode, maxBytes int) (scanOutcome, error) {
	var out scanOutcome
	if call == nil || m == nil {
		return out, nil
	}

	budget := newScanBudget(maxBytes)
	fragments := walkLogicalFragments(call, budget)
	out.BytesScanned = budget.used
	out.ScanLimitHit = budget.limitHit
	for _, fragment := range fragments {
		if err := scanLogicalFragment(ctx, fragment, m, mode, &out); err != nil {
			return out, err
		}
	}
	return out, nil
}

func scanLogicalFragment(ctx context.Context, fragment LogicalFragment, m sdk.Matcher, mode scanMode, out *scanOutcome) error {
	switch mode {
	case modeScan:
		var (
			findings []sdk.Finding
			err      error
		)
		if fragment.Kind == FragmentJSON {
			findings, err = scanJSONPayload(ctx, m, fragment.Raw)
		} else {
			findings, err = m.ScanString(ctx, string(fragment.Raw))
		}
		if err != nil {
			return err
		}
		out.Findings = mergeFindingsAt(out.Findings, findings, fragment.Location)
		out.exactPrivateFindings = append(out.exactPrivateFindings, collectExactPrivateFindings(m, fragment, findings)...)
	case modeRedact:
		var (
			redacted []byte
			findings []sdk.Finding
			err      error
		)
		if fragment.Kind == FragmentJSON {
			redacted, findings, err = redactJSONPayload(ctx, m, fragment.Raw)
		} else {
			var text string
			text, findings, err = m.RedactString(ctx, string(fragment.Raw))
			redacted = []byte(text)
		}
		if err != nil {
			if fragment.Kind == FragmentJSON {
				out.Findings = mergeFindingsAt(out.Findings, findings, fragment.Location)
			}
			out.exactPrivateFindings = append(out.exactPrivateFindings, collectExactPrivateFindingsForRedact(m, fragment, findings)...)
			return err
		}
		out.Findings = mergeFindingsAt(out.Findings, findings, fragment.Location)
		out.exactPrivateFindings = append(out.exactPrivateFindings, collectExactPrivateFindingsForRedact(m, fragment, findings)...)
		if string(redacted) != string(fragment.Raw) {
			fragment.setRaw(redacted)
			out.MutationCount++
		}
	}
	return nil
}

func mergeFindingsAt(dst, src []sdk.Finding, loc string) []sdk.Finding {
	if len(src) == 0 {
		return dst
	}
	tagged := make([]sdk.Finding, len(src))
	for i, f := range src {
		f.Location = loc
		tagged[i] = f
	}
	return mergeFindings(dst, tagged)
}

// mergeFindings merges by Location+SecretRefName, summing OccurrenceCount and
// keeping the first SourceCategory/Aliases.
func mergeFindings(dst, src []sdk.Finding) []sdk.Finding {
	if len(src) == 0 {
		return dst
	}
	type key struct {
		loc string
		ref string
	}
	idx := make(map[key]int, len(dst))
	for i, f := range dst {
		idx[key{loc: f.Location, ref: f.SecretRefName}] = i
	}
	for _, f := range src {
		k := key{loc: f.Location, ref: f.SecretRefName}
		if j, ok := idx[k]; ok {
			dst[j].OccurrenceCount += f.OccurrenceCount
			continue
		}
		idx[k] = len(dst)
		dst = append(dst, f)
	}
	return dst
}
