package secretsguard

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type scanMode int

const (
	modeScan scanMode = iota
	modeRedact
)

type scanOutcome struct {
	Findings      []secretguard.Finding
	MutationCount int
	BytesScanned  int
	ScanLimitHit  bool
}

// scanCall walks model-bound fields exactly once. Locations are stable paths.
func scanCall(ctx context.Context, call *lipapi.Call, m secretguard.Matcher, mode scanMode, maxBytes int) (scanOutcome, error) {
	var out scanOutcome
	if call == nil || m == nil {
		return out, nil
	}
	if maxBytes <= 0 {
		maxBytes = DefaultScanMaxBytes
	}

	for i := range call.Instructions {
		locPrefix := fmt.Sprintf("instructions[%d]", i)
		if err := scanMessageParts(ctx, &call.Instructions[i], locPrefix, m, mode, maxBytes, &out); err != nil {
			return out, err
		}
		if out.ScanLimitHit {
			return out, nil
		}
	}
	for i := range call.Messages {
		locPrefix := fmt.Sprintf("messages[%d]", i)
		if err := scanMessageParts(ctx, &call.Messages[i], locPrefix, m, mode, maxBytes, &out); err != nil {
			return out, err
		}
		if out.ScanLimitHit {
			return out, nil
		}
	}
	for i := range call.Tools {
		if err := scanTool(ctx, &call.Tools[i], i, m, mode, maxBytes, &out); err != nil {
			return out, err
		}
		if out.ScanLimitHit {
			return out, nil
		}
	}
	return out, nil
}

func scanMessageParts(ctx context.Context, msg *lipapi.Message, msgLoc string, m secretguard.Matcher, mode scanMode, maxBytes int, out *scanOutcome) error {
	for j := range msg.Parts {
		loc := fmt.Sprintf("%s.parts[%d]", msgLoc, j)
		if err := scanPart(ctx, &msg.Parts[j], loc, m, mode, maxBytes, out); err != nil {
			return err
		}
		if out.ScanLimitHit {
			return nil
		}
	}
	return nil
}

func scanPart(ctx context.Context, p *lipapi.Part, loc string, m secretguard.Matcher, mode scanMode, maxBytes int, out *scanOutcome) error {
	switch p.Kind {
	case lipapi.PartText:
		return touchText(ctx, &p.Text, loc, m, mode, maxBytes, out)
	case lipapi.PartJSON:
		return touchJSON(ctx, &p.Content, loc, m, mode, maxBytes, out)
	case lipapi.PartToolResult:
		// Text and/or Content; same location; findings merge by Location+SecretRefName.
		if p.Text != "" {
			if err := touchText(ctx, &p.Text, loc, m, mode, maxBytes, out); err != nil {
				return err
			}
			if out.ScanLimitHit {
				return nil
			}
		}
		if len(p.Content) > 0 {
			return touchJSON(ctx, &p.Content, loc, m, mode, maxBytes, out)
		}
		return nil
	default:
		// image_ref, file_ref, unknown — do not scan
		return nil
	}
}

func scanTool(ctx context.Context, tool *lipapi.ToolDef, i int, m secretguard.Matcher, mode scanMode, maxBytes int, out *scanOutcome) error {
	if err := touchText(ctx, &tool.Name, fmt.Sprintf("tools[%d].name", i), m, mode, maxBytes, out); err != nil {
		return err
	}
	if out.ScanLimitHit {
		return nil
	}
	if err := touchText(ctx, &tool.Description, fmt.Sprintf("tools[%d].description", i), m, mode, maxBytes, out); err != nil {
		return err
	}
	if out.ScanLimitHit {
		return nil
	}
	if len(tool.Parameters) > 0 {
		return touchJSON(ctx, &tool.Parameters, fmt.Sprintf("tools[%d].schema", i), m, mode, maxBytes, out)
	}
	return nil
}

func touchText(ctx context.Context, s *string, loc string, m secretguard.Matcher, mode scanMode, maxBytes int, out *scanOutcome) error {
	if s == nil {
		return nil
	}
	n := len(*s)
	if n == 0 {
		return nil
	}
	if !reserveBytes(out, n, maxBytes) {
		return nil
	}
	switch mode {
	case modeScan:
		findings, err := m.ScanString(ctx, *s)
		if err != nil {
			return err
		}
		out.Findings = mergeFindingsAt(out.Findings, findings, loc)
	case modeRedact:
		redacted, findings, err := m.RedactString(ctx, *s)
		if err != nil {
			return err
		}
		if redacted != *s {
			*s = redacted
			out.MutationCount++
		}
		out.Findings = mergeFindingsAt(out.Findings, findings, loc)
	}
	return nil
}

func touchJSON(ctx context.Context, raw *json.RawMessage, loc string, m secretguard.Matcher, mode scanMode, maxBytes int, out *scanOutcome) error {
	if raw == nil || len(*raw) == 0 {
		return nil
	}
	n := len(*raw)
	if !reserveBytes(out, n, maxBytes) {
		return nil
	}
	switch mode {
	case modeScan:
		findings, err := scanJSONPayload(ctx, m, *raw)
		if err != nil {
			return err
		}
		out.Findings = mergeFindingsAt(out.Findings, findings, loc)
	case modeRedact:
		redacted, findings, err := redactJSONPayload(ctx, m, *raw)
		if err != nil {
			out.Findings = mergeFindingsAt(out.Findings, findings, loc)
			return err
		}
		out.Findings = mergeFindingsAt(out.Findings, findings, loc)
		if string(redacted) != string(*raw) {
			*raw = json.RawMessage(redacted)
			out.MutationCount++
		}
	}
	return nil
}

func reserveBytes(out *scanOutcome, n, maxBytes int) bool {
	if out.BytesScanned+n > maxBytes {
		out.ScanLimitHit = true
		return false
	}
	out.BytesScanned += n
	return true
}

func mergeFindingsAt(dst, src []secretguard.Finding, loc string) []secretguard.Finding {
	if len(src) == 0 {
		return dst
	}
	tagged := make([]secretguard.Finding, len(src))
	for i, f := range src {
		f.Location = loc
		tagged[i] = f
	}
	return mergeFindings(dst, tagged)
}

// mergeFindings merges by Location+SecretRefName, summing OccurrenceCount and
// keeping the first SourceCategory/Aliases.
func mergeFindings(dst, src []secretguard.Finding) []secretguard.Finding {
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
