package comparison

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
)

// Aggregates holds only numeric bounded summaries (no content payloads).
// Optional metric pointers must be omitted for blocked/synthetic cells.
type Aggregates struct {
	Samples    int      `json:"samples"`
	Rate       *float64 `json:"rate,omitempty"`
	P50Ms      *float64 `json:"p50_ms,omitempty"`
	P95Ms      *float64 `json:"p95_ms,omitempty"`
	Count      *int     `json:"count,omitempty"`
	MaxLive    *int     `json:"max_live,omitempty"`
	DurationMs *float64 `json:"duration_ms,omitempty"`
}

func (a Aggregates) hasMetrics() bool {
	return a.Rate != nil || a.P50Ms != nil || a.P95Ms != nil ||
		a.Count != nil || a.MaxLive != nil || a.DurationMs != nil
}

// Cell is one connector×dimension observation.
type Cell struct {
	Connector     ConnectorID       `json:"connector"`
	Dimension     Dimension         `json:"dimension"`
	Evidence      EvidenceClass     `json:"evidence_class"`
	Aggregates    Aggregates        `json:"aggregates"`
	Incident      SafeIncidentClass `json:"incident_class,omitempty"`
	BlockedReason BlockedReason     `json:"blocked_reason,omitempty"`
	Note          NoteCode          `json:"note,omitempty"`
}

// InputDocument is the only accepted report input shape.
type InputDocument struct {
	SchemaVersion int    `json:"schema_version"`
	GeneratedAt   string `json:"generated_at,omitempty"`
	Cells         []Cell `json:"cells"`
}

// Report is the bounded comparison output.
type Report struct {
	SchemaVersion     int               `json:"schema_version"`
	GeneratedAt       string            `json:"generated_at"`
	Title             string            `json:"title"`
	ExperimentalNote  string            `json:"experimental_note"`
	ReplacementStatus string            `json:"replacement_status"`
	Cells             []Cell            `json:"cells"`
	Coverage          map[string]string `json:"coverage"`
	Limitations       []string          `json:"limitations"`
}

const SchemaVersion = 1

const experimentalNote = "cursorsdk remains experimental and non-default; cursorcliacp remains available. No default-route switch or ACP deprecation from this report."

const replacementStatusRetainBoth = "retain_both_connectors"

// ValidateInput checks schema, matrix coverage for both connectors, and evidence labels.
func ValidateInput(doc InputDocument) error {
	if doc.SchemaVersion != SchemaVersion {
		return fmt.Errorf("comparison: schema_version must be %d", SchemaVersion)
	}
	if len(doc.Cells) == 0 {
		return fmt.Errorf("comparison: cells required")
	}
	seen := make(map[string]EvidenceClass, len(doc.Cells))
	for i, c := range doc.Cells {
		if err := validateCell(c); err != nil {
			return fmt.Errorf("comparison: cell[%d]: %w", i, err)
		}
		key := cellKey(c.Connector, c.Dimension)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("comparison: duplicate cell %s", key)
		}
		seen[key] = c.Evidence
	}
	for _, conn := range []ConnectorID{ConnectorSDK, ConnectorACP} {
		for _, dim := range RequiredDimensions() {
			key := cellKey(conn, dim)
			if _, ok := seen[key]; !ok {
				return fmt.Errorf("comparison: missing cell %s", key)
			}
		}
	}
	return nil
}

func validateCell(c Cell) error {
	switch c.Connector {
	case ConnectorSDK, ConnectorACP:
	default:
		return fmt.Errorf("unknown connector %q", c.Connector)
	}
	if !validDimension(c.Dimension) {
		return fmt.Errorf("unknown dimension %q", c.Dimension)
	}
	switch c.Evidence {
	case EvidenceMeasured, EvidenceSynthetic, EvidenceBlocked:
	default:
		return fmt.Errorf("unknown evidence_class %q", c.Evidence)
	}
	if c.Aggregates.Samples < 0 {
		return fmt.Errorf("samples must be >= 0")
	}
	if !ValidNoteCode(c.Note) {
		return fmt.Errorf("unknown note code %q", c.Note)
	}
	if c.Incident != "" && !validIncident(c.Incident) {
		return fmt.Errorf("unknown incident_class %q", c.Incident)
	}
	switch c.Evidence {
	case EvidenceBlocked:
		if !ValidBlockedReason(c.BlockedReason) {
			return fmt.Errorf("blocked_reason required when evidence_class=blocked")
		}
		if c.Aggregates.Samples != 0 {
			return fmt.Errorf("blocked cells require samples=0")
		}
		if c.Aggregates.hasMetrics() {
			return fmt.Errorf("blocked cells must omit rate/count/latency fields")
		}
	case EvidenceSynthetic:
		if c.BlockedReason != "" {
			return fmt.Errorf("synthetic cells must omit blocked_reason")
		}
		if c.Aggregates.hasMetrics() {
			return fmt.Errorf("synthetic cells must omit comparative metrics")
		}
	case EvidenceMeasured:
		if c.Aggregates.Samples < 1 {
			return fmt.Errorf("measured cells require samples >= 1")
		}
		if c.BlockedReason != "" {
			return fmt.Errorf("measured cells must omit blocked_reason")
		}
	}
	return nil
}

func validDimension(d Dimension) bool {
	return slices.Contains(RequiredDimensions(), d)
}

func validIncident(i SafeIncidentClass) bool {
	switch i {
	case IncidentNone, IncidentStartupFailure, IncidentPreOutputFailure, IncidentPostOutputFailure,
		IncidentCancelTimeout, IncidentRestart, IncidentLeakSuspected, IncidentContinuityReset,
		IncidentPlatformBlocked, IncidentUpstreamDrift:
		return true
	default:
		return false
	}
}

func cellKey(c ConnectorID, d Dimension) string {
	return string(c) + "|" + string(d)
}

// BuildReport validates and scans input (including hand-built documents) then produces a bounded report.
func BuildReport(doc InputDocument) (Report, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return Report{}, fmt.Errorf("comparison: marshal input: %w", err)
	}
	if len(raw) > MaxInputBytes {
		return Report{}, fmt.Errorf("comparison: input size exceeds %d bytes", MaxInputBytes)
	}
	if err := ScanForbiddenRawJSON(raw); err != nil {
		return Report{}, err
	}
	if err := ValidateInput(doc); err != nil {
		return Report{}, err
	}
	cells := append([]Cell(nil), doc.Cells...)
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].Connector != cells[j].Connector {
			return cells[i].Connector < cells[j].Connector
		}
		return cells[i].Dimension < cells[j].Dimension
	})
	cov := make(map[string]string, len(cells))
	measured := 0
	synthetic := 0
	blocked := 0
	for _, c := range cells {
		cov[cellKey(c.Connector, c.Dimension)] = string(c.Evidence)
		switch c.Evidence {
		case EvidenceMeasured:
			measured++
		case EvidenceSynthetic:
			synthetic++
		case EvidenceBlocked:
			blocked++
		}
	}
	generated := strings.TrimSpace(doc.GeneratedAt)
	if generated == "" {
		generated = time.Now().UTC().Format(time.RFC3339)
	}
	lim := []string{
		"Comparative dogfood with live credentials remains blocked until CURSOR_SDK_LIVE=1 and CURSOR_API_KEY plus an intentional ACP dogfood lane are opted in.",
		"Default fixture evidence is synthetic or blocked without numeric comparative metrics; it does not establish SDK superiority or justify default-route migration.",
		fmt.Sprintf("Evidence mix: measured=%d synthetic=%d blocked=%d", measured, synthetic, blocked),
	}
	rep := Report{
		SchemaVersion:     SchemaVersion,
		GeneratedAt:       generated,
		Title:             "Cursor ACP vs SDK comparison matrix",
		ExperimentalNote:  experimentalNote,
		ReplacementStatus: replacementStatusRetainBoth,
		Cells:             cells,
		Coverage:          cov,
		Limitations:       lim,
	}
	out, err := json.Marshal(rep)
	if err != nil {
		return Report{}, fmt.Errorf("comparison: marshal report: %w", err)
	}
	if len(out) > MaxReportBytes {
		return Report{}, fmt.Errorf("comparison: report size exceeds %d bytes", MaxReportBytes)
	}
	if err := scanForbiddenContent(out); err != nil {
		return Report{}, fmt.Errorf("comparison: report scan: %w", err)
	}
	return rep, nil
}

// WriteJSON writes the report as JSON.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteMarkdown writes a bounded human-readable report.
func WriteMarkdown(w io.Writer, r Report) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# %s\n\n", r.Title)
	fmt.Fprintf(&b, "- Generated: `%s`\n", r.GeneratedAt)
	fmt.Fprintf(&b, "- Schema: `%d`\n", r.SchemaVersion)
	fmt.Fprintf(&b, "- Replacement status: `%s`\n", r.ReplacementStatus)
	fmt.Fprintf(&b, "- %s\n\n", r.ExperimentalNote)
	fmt.Fprintf(&b, "## Matrix\n\n")
	fmt.Fprintf(&b, "| Connector | Dimension | Evidence | Samples | Incident | Notes |\n")
	fmt.Fprintf(&b, "| --- | --- | --- | ---: | --- | --- |\n")
	for _, c := range r.Cells {
		note := string(c.Note)
		if c.Evidence == EvidenceBlocked && c.BlockedReason != "" {
			if note != "" {
				note = string(c.BlockedReason) + "; " + note
			} else {
				note = string(c.BlockedReason)
			}
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | `%s` | %s |\n",
			c.Connector, c.Dimension, c.Evidence, samplesCell(c.Aggregates.Samples), emptyDash(string(c.Incident)), escapeMD(note))
	}
	fmt.Fprintf(&b, "\n## Limitations\n\n")
	for _, lim := range r.Limitations {
		fmt.Fprintf(&b, "- %s\n", lim)
	}
	if b.Len() > MaxReportBytes {
		return fmt.Errorf("comparison: report size exceeds %d bytes", MaxReportBytes)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func samplesCell(n int) string {
	if n <= 0 {
		return "`-`"
	}
	return fmt.Sprintf("%d", n)
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func escapeMD(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// DecodeInputJSON decodes and rejects unknown top-level keys via strict decoder.
func DecodeInputJSON(r io.Reader) (InputDocument, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var doc InputDocument
	if err := dec.Decode(&doc); err != nil {
		return InputDocument{}, fmt.Errorf("comparison: decode: %w", err)
	}
	return doc, nil
}
