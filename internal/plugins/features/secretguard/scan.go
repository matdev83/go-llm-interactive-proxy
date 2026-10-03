package secretguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type scanMode int

const (
	modeScan scanMode = iota
	modeRedact
)

type scanOutcome struct {
	Findings      []sdk.Finding
	MutationCount int
	BytesScanned  int
	ScanLimitHit  bool
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
			return err
		}
		out.Findings = mergeFindingsAt(out.Findings, findings, fragment.Location)
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
