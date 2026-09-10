package largebody

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// redactedSensitive is the only rendering of a sensitive value outside its
// owning frontend boundary.
const redactedSensitive = "[redacted]"

// Source is an immutable completed replay capture (design section 5).
// After EOF the source exposes independent offset-zero readers; each
// provider/credential retry opens and closes a fresh reader. The concrete
// spool implementation (Task 4) lives elsewhere; this is the contract seam.
type Source interface {
	// Size reports the exact captured body size in bytes.
	Size() int64
	// Open returns an independent offset-zero reader over the capture.
	Open() (io.ReadCloser, error)
	// Close releases the root capture; it is idempotent.
	Close() error
}

// Span is an exact raw byte range inside the captured body, used for
// certified rewrite splices such as the top-level model token
// (Requirements 4, 9).
type Span struct {
	Offset int64
	Length int64
}

// Validate rejects negative bounds and checked-int64 overflow.
func (s Span) Validate() error {
	if s.Offset < 0 {
		return fmt.Errorf("largebody: span offset must be >= 0, got %d", s.Offset)
	}
	if s.Length < 0 {
		return fmt.Errorf("largebody: span length must be >= 0, got %d", s.Length)
	}
	if _, err := s.End(); err != nil {
		return err
	}
	return nil
}

// End returns the exclusive end offset using checked int64 math.
func (s Span) End() (int64, error) {
	if s.Offset > math.MaxInt64-s.Length {
		return 0, fmt.Errorf("largebody: span end overflows int64 (offset %d length %d)", s.Offset, s.Length)
	}
	return s.Offset + s.Length, nil
}

// CheckedSpliceLength returns prefixLen+replacementLen+suffixLen using
// checked int64 math before provider open (Requirement 9.4).
func CheckedSpliceLength(prefixLen, replacementLen, suffixLen int64) (int64, error) {
	for name, v := range map[string]int64{"prefix": prefixLen, "replacement": replacementLen, "suffix": suffixLen} {
		if v < 0 {
			return 0, fmt.Errorf("largebody: splice %s length must be >= 0, got %d", name, v)
		}
	}
	partial, err := checkedAdd(prefixLen, replacementLen)
	if err != nil {
		return 0, err
	}
	return checkedAdd(partial, suffixLen)
}

func checkedAdd(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, fmt.Errorf("largebody: length addition overflows int64 (%d + %d)", a, b)
	}
	return a + b, nil
}

// BodyMode names the certified body representation (design sections 6, 9).
// Wave 1 supports identity JSON only; decoded/compressed modes need a
// separate certification wave and are rejected here.
type BodyMode string

const (
	// BodyModeUnknown is the zero value and never validates.
	BodyModeUnknown BodyMode = ""
	// BodyModeIdentityJSON is an uncompressed identity JSON body.
	BodyModeIdentityJSON BodyMode = "identity_json"
)

// String returns a bounded static label for metrics/diagnostics.
func (m BodyMode) String() string {
	if m == BodyModeIdentityJSON {
		return string(m)
	}
	return "unknown"
}

// Validate accepts only certified body modes.
func (m BodyMode) Validate() error {
	if m != BodyModeIdentityJSON {
		return fmt.Errorf("largebody: unsupported body mode %q", string(m))
	}
	return nil
}

// RewriteKind classifies the certified rewrite applied to one attempt.
type RewriteKind uint8

const (
	// RewriteKindNone means the captured bytes are forwarded unchanged.
	RewriteKindNone RewriteKind = iota
	// RewriteKindModelToken means the scanner-recorded top-level model token
	// span is spliced with a JSON-encoded replacement model.
	RewriteKindModelToken
)

// String returns a bounded static label for metrics/diagnostics.
func (k RewriteKind) String() string {
	switch k {
	case RewriteKindNone:
		return "none"
	case RewriteKindModelToken:
		return "model_token"
	default:
		return "unknown"
	}
}

// RewriteSemantics is the immutable certified rewrite contract passed to
// every backend exact/domain resolver (design section 9). A resolver may
// declare NeedsModelRewrite only when the profile certified it here; the
// flag can never grant an uncertified transformation.
type RewriteSemantics struct {
	kind RewriteKind
	span Span
}

// NewNoRewrite returns the immutable no-rewrite contract.
func NewNoRewrite() RewriteSemantics {
	return RewriteSemantics{kind: RewriteKindNone}
}

// NewModelTokenRewrite returns the immutable model-token splice contract
// for an exact, non-empty scanner-recorded span.
func NewModelTokenRewrite(span Span) (RewriteSemantics, error) {
	if err := span.Validate(); err != nil {
		return RewriteSemantics{}, fmt.Errorf("largebody: model rewrite: %w", err)
	}
	if span.Length == 0 {
		return RewriteSemantics{}, fmt.Errorf("largebody: model rewrite span must be non-empty")
	}
	return RewriteSemantics{kind: RewriteKindModelToken, span: span}, nil
}

// Kind reports the certified rewrite kind.
func (r RewriteSemantics) Kind() RewriteKind { return r.kind }

// Span returns the certified rewrite span (meaningful for model-token rewrites).
func (r RewriteSemantics) Span() Span { return r.span }

// NeedsModelRewrite reports whether attempts must splice a replacement model.
func (r RewriteSemantics) NeedsModelRewrite() bool { return r.kind == RewriteKindModelToken }

// Validate rejects unknown kinds and invalid spans.
func (r RewriteSemantics) Validate() error {
	switch r.kind {
	case RewriteKindNone:
		return nil
	case RewriteKindModelToken:
		if err := r.span.Validate(); err != nil {
			return fmt.Errorf("largebody: model rewrite: %w", err)
		}
		if r.span.Length == 0 {
			return fmt.Errorf("largebody: model rewrite span must be non-empty")
		}
		return nil
	default:
		return fmt.Errorf("largebody: unknown rewrite kind %d", uint8(r.kind))
	}
}

// SensitiveString is an opaque bearer value (resume tokens) that must never
// reach backends, telemetry, logs, or metrics (Requirements 14, 22). Every
// default rendering is redacted; the owning frontend extracts the value via
// Reveal to emit authoritative session/resume headers.
type SensitiveString struct {
	value string
}

// NewSensitiveString wraps a bearer value without exposing it.
func NewSensitiveString(value string) SensitiveString {
	return SensitiveString{value: value}
}

// Reveal returns the wrapped value for the owning frontend boundary.
func (s SensitiveString) Reveal() string { return s.value }

// IsZero reports whether no value is wrapped.
func (s SensitiveString) IsZero() bool { return s.value == "" }

// String redacts the value.
func (s SensitiveString) String() string { return redactedSensitive }

// GoString redacts the value.
func (s SensitiveString) GoString() string { return redactedSensitive }

// Format redacts the value for every verb.
func (s SensitiveString) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, redactedSensitive)
}

// MarshalJSON redacts the value.
func (s SensitiveString) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedSensitive)
}

// MarshalText redacts the value.
func (s SensitiveString) MarshalText() ([]byte, error) {
	return []byte(redactedSensitive), nil
}

// ProtocolFacts carries profile-certified protocol requirements as bounded
// scalar facts (design section 6). RequirementsID is a bounded static
// profile-defined requirement-set label (never user/model/session text);
// ControlCount counts certified control carriers, never prompt content.
type ProtocolFacts struct {
	RequirementsID string
	ControlCount   int64
}

// Validate enforces scalar bounds under the semantic-fact budget.
func (f ProtocolFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if int64(len(f.RequirementsID)) > maxFactBytes {
		return fmt.Errorf("largebody: protocol requirements id exceeds %d bytes", maxFactBytes)
	}
	if f.ControlCount < 0 {
		return fmt.Errorf("largebody: protocol control count must be >= 0, got %d", f.ControlCount)
	}
	return nil
}

// SessionInput carries the exact bounded client/session authority inputs
// that canonical BeginTurn receives, sourced through normal
// frontend/header/body precedence (Requirement 14.1). The resume token is
// sensitive: it never reaches backends or telemetry.
type SessionInput struct {
	AuthoritativeSessionID string
	ClientSessionID        string
	ALegID                 string
	ResumeToken            SensitiveString
	NewSessionRequested    bool
}

// Validate enforces scalar bounds under the semantic-fact budget.
func (s SessionInput) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"authoritative session id": s.AuthoritativeSessionID,
		"client session id":        s.ClientSessionID,
		"a-leg id":                 s.ALegID,
	} {
		if int64(len(v)) > maxFactBytes {
			return fmt.Errorf("largebody: session %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	if int64(len(s.ResumeToken.Reveal())) > maxFactBytes {
		return fmt.Errorf("largebody: session resume token exceeds %d bytes", maxFactBytes)
	}
	return nil
}

// MarshalJSON redacts the resume token while preserving presence signals.
func (s SessionInput) MarshalJSON() ([]byte, error) {
	token := ""
	if !s.ResumeToken.IsZero() {
		token = redactedSensitive
	}
	return json.Marshal(struct {
		AuthoritativeSessionID string `json:"authoritative_session_id,omitempty"`
		ClientSessionID        string `json:"client_session_id,omitempty"`
		ALegID                 string `json:"aleg_id,omitempty"`
		ResumeToken            string `json:"resume_token,omitempty"`
		NewSessionRequested    bool   `json:"new_session_requested,omitempty"`
	}{
		AuthoritativeSessionID: s.AuthoritativeSessionID,
		ClientSessionID:        s.ClientSessionID,
		ALegID:                 s.ALegID,
		ResumeToken:            token,
		NewSessionRequested:    s.NewSessionRequested,
	})
}

// ClientTurnPartShape describes one normalized content part by kind and
// attributed byte size only. Prompt text is never materialized for the
// recorder (Requirement 14.5).
type ClientTurnPartShape struct {
	Kind         lipapi.ContentPartKind
	ContentBytes int64
}

// Validate enforces canonical vocabulary membership and non-negative sizes.
func (p ClientTurnPartShape) Validate() error {
	switch p.Kind {
	case lipapi.ContentPartText,
		lipapi.ContentPartImageRef,
		lipapi.ContentPartFileRef,
		lipapi.ContentPartVideoRef,
		lipapi.ContentPartRefusal,
		lipapi.ContentPartReasoning,
		lipapi.ContentPartSummary,
		lipapi.ContentPartAnnotation,
		lipapi.ContentPartAssistantRef,
		lipapi.ContentPartJSON,
		lipapi.ContentPartToolResult,
		lipapi.ContentPartExtension:
	default:
		return fmt.Errorf("largebody: unknown turn part kind %q", string(p.Kind))
	}
	if p.ContentBytes < 0 {
		return fmt.Errorf("largebody: turn part content bytes must be >= 0, got %d", p.ContentBytes)
	}
	return nil
}

// ClientTurnItemShape describes one normalized turn item by kind, role,
// ordinal, and part shapes only.
type ClientTurnItemShape struct {
	Kind    lipapi.ItemKind
	Role    lipapi.Role
	Ordinal int64
	Parts   []ClientTurnPartShape
}

// Validate enforces canonical vocabulary membership and bounds part counts
// under the semantic-fact budget.
func (it ClientTurnItemShape) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	switch it.Kind {
	case lipapi.ItemKindMessage,
		lipapi.ItemKindItemReference,
		lipapi.ItemKindToolCall,
		lipapi.ItemKindToolResult,
		lipapi.ItemKindReasoning,
		lipapi.ItemKindCompaction,
		lipapi.ItemKindExtension:
	default:
		return fmt.Errorf("largebody: unknown turn item kind %q", string(it.Kind))
	}
	if it.Kind == lipapi.ItemKindMessage {
		switch it.Role {
		case lipapi.RoleSystem,
			lipapi.RoleDeveloper,
			lipapi.RoleUser,
			lipapi.RoleAssistant,
			lipapi.RoleTool:
		default:
			return fmt.Errorf("largebody: unknown turn role %q", string(it.Role))
		}
	} else if it.Role != "" {
		switch it.Role {
		case lipapi.RoleSystem,
			lipapi.RoleDeveloper,
			lipapi.RoleUser,
			lipapi.RoleAssistant,
			lipapi.RoleTool,
			lipapi.Role(it.Kind):
		default:
			return fmt.Errorf("largebody: unknown turn role %q", string(it.Role))
		}
	}
	if it.Ordinal < 0 {
		return fmt.Errorf("largebody: turn ordinal must be >= 0, got %d", it.Ordinal)
	}
	if int64(len(it.Parts)) > maxFactBytes {
		return fmt.Errorf("%w: turn part count (%d) exceeds budget %d", ErrSemanticFactBudgetExceeded, len(it.Parts), maxFactBytes)
	}
	for i := range it.Parts {
		if err := it.Parts[i].Validate(); err != nil {
			return fmt.Errorf("largebody: turn part %d: %w", i, err)
		}
	}
	return nil
}

// ClientTurnShape is the bounded normalized client-turn shape equivalent to
// lipapi.NormalizedItems for the certified subset: role/ordinal/content-part
// kinds and other recorder-required non-content facts (Requirement 14.3).
// Semantic-fact budget overflow selects canonical processing.
type ClientTurnShape struct {
	Items             []ClientTurnItemShape
	TotalContentBytes int64
}

// Validate bounds item counts under the semantic-fact budget.
func (s ClientTurnShape) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if s.TotalContentBytes < 0 {
		return fmt.Errorf("largebody: total content bytes must be >= 0, got %d", s.TotalContentBytes)
	}
	if int64(len(s.Items)) > maxFactBytes {
		return fmt.Errorf("%w: turn item count (%d) exceeds budget %d", ErrSemanticFactBudgetExceeded, len(s.Items), maxFactBytes)
	}
	if s.MetadataBytes() > maxFactBytes {
		return fmt.Errorf("%w: turn metadata bytes (%d) exceeds budget %d", ErrSemanticFactBudgetExceeded, s.MetadataBytes(), maxFactBytes)
	}
	for i := range s.Items {
		if err := s.Items[i].Validate(maxFactBytes); err != nil {
			return fmt.Errorf("largebody: turn item %d: %w", i, err)
		}
	}
	return nil
}

// IdentityDigest is the exact canonical semantic identity digest equivalent
// to the post-frontend-decode/pre-core canonical Call identity for the
// supported subset, derived without retaining prompt content
// (Requirement 16). It is a distinct type from SourceDigest so a raw-body
// hash can never substitute for canonical economic/request identity
// (Requirement 16.5).
type IdentityDigest struct {
	sum [32]byte
}

// NewIdentityDigest wraps an already-computed canonical sum.
func NewIdentityDigest(sum [32]byte) IdentityDigest {
	return IdentityDigest{sum: sum}
}

// Sum returns the wrapped canonical sum.
func (d IdentityDigest) Sum() [32]byte { return d.sum }

// IsZero reports whether no sum was provided.
func (d IdentityDigest) IsZero() bool { return d.sum == [32]byte{} }

// String renders the sum as hex for diagnostics.
func (d IdentityDigest) String() string { return hex.EncodeToString(d.sum[:]) }

// SourceDigest is the replay/attempt source-integrity evidence. It is
// explicitly not a substitute for canonical identity (Requirement 16.5,
// design section 6).
type SourceDigest struct {
	sum [32]byte
}

// NewSourceDigest wraps an already-computed source integrity sum.
func NewSourceDigest(sum [32]byte) SourceDigest {
	return SourceDigest{sum: sum}
}

// Sum returns the wrapped source sum.
func (d SourceDigest) Sum() [32]byte { return d.sum }

// IsZero reports whether no sum was provided.
func (d SourceDigest) IsZero() bool { return d.sum == [32]byte{} }

// String renders the sum as hex for diagnostics.
func (d SourceDigest) String() string { return hex.EncodeToString(d.sum[:]) }

// Proof is the bounded frontend profile proof over the validated replay
// stream (design section 6). Every field has a named downstream consumer;
// this must never grow into a shadow lipapi.Call (design section 11).
type Proof struct {
	ProfileID       string
	Operation       lipapi.Operation
	Delivery        lipapi.DeliveryMode
	RouteSelector   string
	ClientModel     string
	MaxOutputTokens int64
	Facts           ProtocolFacts
	Mode            BodyMode
	Rewrite         RewriteSemantics
	ModelSpan       Span
	Identity        IdentityDigest
	Turn            ClientTurnShape
	Session         SessionInput
	Source          SourceDigest
	BodyBytes       int64
}

// Validate enforces bounds and cross-field consistency: a recorded model
// span requires certified model-rewrite semantics (Requirement 9.5
// duplicate/ambiguous forms stay canonical unless certified).
func (p Proof) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if strings.TrimSpace(p.ProfileID) == "" {
		return fmt.Errorf("largebody: proof profile id must not be empty")
	}
	if int64(len(p.ProfileID)) > maxFactBytes {
		return fmt.Errorf("largebody: proof profile id exceeds %d bytes", maxFactBytes)
	}
	if strings.TrimSpace(string(p.Operation)) == "" {
		return fmt.Errorf("largebody: proof operation must not be empty")
	}
	if int64(len(p.Operation)) > maxFactBytes {
		return fmt.Errorf("largebody: proof operation exceeds %d bytes", maxFactBytes)
	}
	switch p.Delivery {
	case lipapi.DeliveryModeStreaming, lipapi.DeliveryModeNonStreaming:
	default:
		return fmt.Errorf("largebody: proof delivery mode %q is unknown", string(p.Delivery))
	}
	if int64(len(p.RouteSelector)) > maxFactBytes {
		return fmt.Errorf("largebody: proof route selector exceeds %d bytes", maxFactBytes)
	}
	if int64(len(p.ClientModel)) > maxFactBytes {
		return fmt.Errorf("largebody: proof client model exceeds %d bytes", maxFactBytes)
	}
	if p.MaxOutputTokens < 0 {
		return fmt.Errorf("largebody: proof max output tokens must be >= 0, got %d", p.MaxOutputTokens)
	}
	if err := p.Facts.Validate(maxFactBytes); err != nil {
		return err
	}
	if err := p.Mode.Validate(); err != nil {
		return err
	}
	if err := p.Rewrite.Validate(); err != nil {
		return err
	}
	if err := p.ModelSpan.Validate(); err != nil {
		return err
	}
	if p.Rewrite.NeedsModelRewrite() && p.ModelSpan.Length == 0 {
		return fmt.Errorf("largebody: proof rewrite requires a non-empty model span")
	}
	if !p.Rewrite.NeedsModelRewrite() && p.ModelSpan != (Span{}) {
		return fmt.Errorf("largebody: proof model span requires certified rewrite semantics")
	}
	if p.Identity.IsZero() {
		return fmt.Errorf("largebody: proof canonical identity digest must not be zero")
	}
	if err := p.Turn.Validate(maxFactBytes); err != nil {
		return err
	}
	if err := p.Session.Validate(maxFactBytes); err != nil {
		return err
	}
	if p.Source.IsZero() {
		return fmt.Errorf("largebody: proof source digest must not be zero")
	}
	if p.BodyBytes <= 0 {
		return fmt.Errorf("largebody: proof body bytes must be > 0, got %d", p.BodyBytes)
	}
	return nil
}

// AssessmentRequest carries proof plus immutable generation facts only
// (design section 8). It never carries a body reader: assessment inspects
// bounded facts, not prompt content (Requirement 6.2).
type AssessmentRequest struct {
	Proof        Proof
	GenerationID string
}

// Validate enforces the generation binding and proof bounds.
func (r AssessmentRequest) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if strings.TrimSpace(r.GenerationID) == "" {
		return fmt.Errorf("largebody: assessment generation binding must not be empty")
	}
	if int64(len(r.GenerationID)) > maxFactBytes {
		return fmt.Errorf("largebody: assessment generation binding exceeds %d bytes", maxFactBytes)
	}
	if err := r.Proof.Validate(maxFactBytes); err != nil {
		return err
	}
	return nil
}

// AssessmentDecision is the bounded assessment outcome.
type AssessmentDecision uint8

const (
	// AssessmentDecisionUnknown is the zero value and never validates.
	AssessmentDecisionUnknown AssessmentDecision = iota
	// AssessmentDecisionDecline selects canonical decode under the held permit.
	AssessmentDecisionDecline
	// AssessmentDecisionAccept crosses the one-way wire commit.
	AssessmentDecisionAccept
)

// String returns a bounded static label for metrics/diagnostics.
func (d AssessmentDecision) String() string {
	switch d {
	case AssessmentDecisionDecline:
		return "decline"
	case AssessmentDecisionAccept:
		return "accept"
	default:
		return "unknown"
	}
}

// DeclineReason is the bounded assessment-decline taxonomy for diagnostics.
// Labels are static; backend/model/session/user IDs never appear here
// (Requirement 22.2).
type DeclineReason uint16

const (
	// DeclineReasonNone accompanies accept decisions only.
	DeclineReasonNone DeclineReason = iota
	// DeclineReasonProofUncertain means profile proof could not certify the shape.
	DeclineReasonProofUncertain
	// DeclineReasonAuthorityBlocker means a frozen authority blocks wire mode.
	DeclineReasonAuthorityBlocker
	// DeclineReasonRouteIncompatible means the route domain is not wire-compatible.
	DeclineReasonRouteIncompatible
	// DeclineReasonBackendIncompatible means a candidate lacks exact/domain proof.
	DeclineReasonBackendIncompatible
	// DeclineReasonRewriteUnsupported means the rewrite has no certified contract.
	DeclineReasonRewriteUnsupported
	// DeclineReasonSessionUnsupported means session facts lack a wire view.
	DeclineReasonSessionUnsupported
	// DeclineReasonMeteringUnsupported means metering lacks a wire-native path.
	DeclineReasonMeteringUnsupported
	// DeclineReasonCountingUnsupported means token counting lacks an exact wire contract.
	DeclineReasonCountingUnsupported
	// DeclineReasonGenerationMismatch means execution disagrees with the stamp.
	DeclineReasonGenerationMismatch
)

// String returns a bounded static label for metrics/diagnostics.
func (r DeclineReason) String() string {
	switch r {
	case DeclineReasonNone:
		return "none"
	case DeclineReasonProofUncertain:
		return "proof_uncertain"
	case DeclineReasonAuthorityBlocker:
		return "authority_blocker"
	case DeclineReasonRouteIncompatible:
		return "route_incompatible"
	case DeclineReasonBackendIncompatible:
		return "backend_incompatible"
	case DeclineReasonRewriteUnsupported:
		return "rewrite_unsupported"
	case DeclineReasonSessionUnsupported:
		return "session_unsupported"
	case DeclineReasonMeteringUnsupported:
		return "metering_unsupported"
	case DeclineReasonCountingUnsupported:
		return "counting_unsupported"
	case DeclineReasonGenerationMismatch:
		return "generation_mismatch"
	default:
		return "unknown"
	}
}

// AssessmentStamp is the opaque generation/proof-bound acceptance stamp
// (design sections 8, 11.8). Execution revalidates it and treats
// disagreement as an invariant failure, never fallback (Requirement 6.7).
type AssessmentStamp struct {
	generationID string
	profileID    string
	source       SourceDigest
	bodyBytes    int64
	mode         BodyMode
	rewrite      RewriteSemantics
	identity     IdentityDigest
}

// NewAssessmentStamp binds generation identity, profile/proof identity,
// source digest/size, and the body/rewrite contract. Budget-bounded length
// checks run in Validate; the constructor enforces structural binding only.
func NewAssessmentStamp(generationID, profileID string, source SourceDigest, bodyBytes int64, mode BodyMode, rewrite RewriteSemantics, identity IdentityDigest) (AssessmentStamp, error) {
	stamp := AssessmentStamp{
		generationID: generationID,
		profileID:    profileID,
		source:       source,
		bodyBytes:    bodyBytes,
		mode:         mode,
		rewrite:      rewrite,
		identity:     identity,
	}
	if err := stamp.validateStructure(); err != nil {
		return AssessmentStamp{}, err
	}
	return stamp, nil
}

// GenerationID returns the bound generation identity.
func (s AssessmentStamp) GenerationID() string { return s.generationID }

// ProfileID returns the bound profile identity.
func (s AssessmentStamp) ProfileID() string { return s.profileID }

// SourceDigest returns the bound source evidence.
func (s AssessmentStamp) SourceDigest() SourceDigest { return s.source }

// BodyBytes returns the bound body size.
func (s AssessmentStamp) BodyBytes() int64 { return s.bodyBytes }

// BodyMode returns the bound body contract.
func (s AssessmentStamp) BodyMode() BodyMode { return s.mode }

// Rewrite returns the bound rewrite contract.
func (s AssessmentStamp) Rewrite() RewriteSemantics { return s.rewrite }

// IdentityDigest returns the bound canonical identity.
func (s AssessmentStamp) IdentityDigest() IdentityDigest { return s.identity }

// Validate enforces the stamp binding under the semantic-fact budget.
func (s AssessmentStamp) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if err := s.validateStructure(); err != nil {
		return err
	}
	if int64(len(s.generationID)) > maxFactBytes || int64(len(s.profileID)) > maxFactBytes {
		return fmt.Errorf("largebody: stamp binding exceeds %d bytes", maxFactBytes)
	}
	return nil
}

func (s AssessmentStamp) validateStructure() error {
	if strings.TrimSpace(s.generationID) == "" {
		return fmt.Errorf("largebody: stamp generation binding must not be empty")
	}
	if strings.TrimSpace(s.profileID) == "" {
		return fmt.Errorf("largebody: stamp profile binding must not be empty")
	}
	if s.source.IsZero() {
		return fmt.Errorf("largebody: stamp source digest must not be zero")
	}
	if s.bodyBytes <= 0 {
		return fmt.Errorf("largebody: stamp body bytes must be > 0, got %d", s.bodyBytes)
	}
	if err := s.mode.Validate(); err != nil {
		return err
	}
	if err := s.rewrite.Validate(); err != nil {
		return err
	}
	if s.identity.IsZero() {
		return fmt.Errorf("largebody: stamp canonical identity digest must not be zero")
	}
	return nil
}

// AssessmentResult is the bounded assessment outcome: an opaque stamp plus
// bounded facts only (design section 8).
type AssessmentResult struct {
	Decision AssessmentDecision
	Reason   DeclineReason
	Stamp    AssessmentStamp
}

// Validate enforces decision/reason/stamp consistency.
func (r AssessmentResult) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	switch r.Decision {
	case AssessmentDecisionDecline:
		if r.Reason == DeclineReasonNone || r.Reason.String() == "unknown" {
			return fmt.Errorf("largebody: decline requires a bounded reason")
		}
		return nil
	case AssessmentDecisionAccept:
		if r.Reason != DeclineReasonNone {
			return fmt.Errorf("largebody: accept must not carry a decline reason")
		}
		if err := r.Stamp.Validate(maxFactBytes); err != nil {
			return fmt.Errorf("largebody: accept: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("largebody: unknown assessment decision %d", uint8(r.Decision))
	}
}

// WireRequestFacts is the immutable provider-neutral input to a backend
// exact wire proof (design section 9). Outbound header construction stays
// backend-owned; no client headers travel here.
type WireRequestFacts struct {
	ProfileID       string
	Operation       lipapi.Operation
	Delivery        lipapi.DeliveryMode
	BodyMode        BodyMode
	Rewrite         RewriteSemantics
	ClientModel     string
	CandidateModel  string
	MaxOutputTokens int64
}

// Validate enforces bounds under the semantic-fact budget.
func (f WireRequestFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if strings.TrimSpace(f.ProfileID) == "" {
		return fmt.Errorf("largebody: wire facts profile id must not be empty")
	}
	if strings.TrimSpace(string(f.Operation)) == "" {
		return fmt.Errorf("largebody: wire facts operation must not be empty")
	}
	switch f.Delivery {
	case lipapi.DeliveryModeStreaming, lipapi.DeliveryModeNonStreaming:
	default:
		return fmt.Errorf("largebody: wire facts delivery mode %q is unknown", string(f.Delivery))
	}
	if err := f.BodyMode.Validate(); err != nil {
		return err
	}
	if err := f.Rewrite.Validate(); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"profile id":      f.ProfileID,
		"operation":       string(f.Operation),
		"client model":    f.ClientModel,
		"candidate model": f.CandidateModel,
	} {
		if int64(len(v)) > maxFactBytes {
			return fmt.Errorf("largebody: wire facts %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	if f.MaxOutputTokens < 0 {
		return fmt.Errorf("largebody: wire facts max output tokens must be >= 0, got %d", f.MaxOutputTokens)
	}
	return nil
}

// WireDomainFacts is the provider-neutral input to a backend late-route
// domain proof (design section 9, Requirement 7). A universal domain
// (AnyAcceptedModel) must not also enumerate models, and a finite domain
// must enumerate at least one proven model.
type WireDomainFacts struct {
	ProfileID       string
	Operation       lipapi.Operation
	Delivery        lipapi.DeliveryMode
	BodyMode        BodyMode
	UniversalModel  bool
	CandidateModels []string
}

// Validate enforces universal/finite exclusivity and bounds.
func (f WireDomainFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if strings.TrimSpace(f.ProfileID) == "" {
		return fmt.Errorf("largebody: wire domain profile id must not be empty")
	}
	if strings.TrimSpace(string(f.Operation)) == "" {
		return fmt.Errorf("largebody: wire domain operation must not be empty")
	}
	switch f.Delivery {
	case lipapi.DeliveryModeStreaming, lipapi.DeliveryModeNonStreaming:
	default:
		return fmt.Errorf("largebody: wire domain delivery mode %q is unknown", string(f.Delivery))
	}
	if err := f.BodyMode.Validate(); err != nil {
		return err
	}
	if int64(len(f.ProfileID)) > maxFactBytes || int64(len(f.Operation)) > maxFactBytes {
		return fmt.Errorf("largebody: wire domain identity exceeds %d bytes", maxFactBytes)
	}
	if f.UniversalModel && len(f.CandidateModels) > 0 {
		return fmt.Errorf("largebody: universal wire domain must not enumerate models")
	}
	if !f.UniversalModel && len(f.CandidateModels) == 0 {
		return fmt.Errorf("largebody: finite wire domain must enumerate at least one model")
	}
	if int64(len(f.CandidateModels)) > maxFactBytes {
		return fmt.Errorf("largebody: wire domain model count exceeds %d", maxFactBytes)
	}
	for i, model := range f.CandidateModels {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("largebody: wire domain model %d must not be empty", i)
		}
		if int64(len(model)) > maxFactBytes {
			return fmt.Errorf("largebody: wire domain model %d exceeds %d bytes", i, maxFactBytes)
		}
	}
	return nil
}

// RewritePlan is the per-attempt certified splice: exact scanner span plus
// JSON-encoded replacement model plus the checked rewritten body length.
// A splice reader emits prefix + replacement + suffix without a second full
// body (Requirement 9.3).
type RewritePlan struct {
	Rewrite          RewriteSemantics
	ReplacementModel string
	RewrittenLength  int64
}

// Validate requires certified rewrite semantics, a bounded replacement
// model, and a positive rewritten length.
func (p RewritePlan) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if err := p.Rewrite.Validate(); err != nil {
		return err
	}
	if !p.Rewrite.NeedsModelRewrite() {
		return fmt.Errorf("largebody: rewrite plan requires certified model-rewrite semantics")
	}
	if strings.TrimSpace(p.ReplacementModel) == "" {
		return fmt.Errorf("largebody: rewrite plan replacement model must not be empty")
	}
	if int64(len(p.ReplacementModel)) > maxFactBytes {
		return fmt.Errorf("largebody: rewrite plan replacement model exceeds %d bytes", maxFactBytes)
	}
	if p.RewrittenLength <= 0 {
		return fmt.Errorf("largebody: rewrite plan rewritten length must be > 0, got %d", p.RewrittenLength)
	}
	return nil
}

// ResponseFacts carries bounded provider-neutral response facts for
// frontend wrapping/encoding (Requirement 18.2): request/trace identity,
// A-leg/session facts, operation/delivery, and replay/rewrite evidence.
// The sensitive session-response carrier travels separately.
type ResponseFacts struct {
	RequestID       string
	TraceID         string
	ALegID          string
	SessionID       string
	Operation       lipapi.Operation
	Delivery        lipapi.DeliveryMode
	EffectiveModel  string
	Source          SourceDigest
	BodyBytes       int64
	RewrittenLength int64
}

// Validate enforces identity presence and bounds.
func (f ResponseFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"request id":      f.RequestID,
		"trace id":        f.TraceID,
		"a-leg id":        f.ALegID,
		"session id":      f.SessionID,
		"operation":       string(f.Operation),
		"effective model": f.EffectiveModel,
	} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("largebody: response facts %s must not be empty", name)
		}
		if int64(len(v)) > maxFactBytes {
			return fmt.Errorf("largebody: response facts %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	switch f.Delivery {
	case lipapi.DeliveryModeStreaming, lipapi.DeliveryModeNonStreaming:
	default:
		return fmt.Errorf("largebody: response facts delivery mode %q is unknown", string(f.Delivery))
	}
	if f.Source.IsZero() {
		return fmt.Errorf("largebody: response facts source digest must not be zero")
	}
	if f.BodyBytes <= 0 {
		return fmt.Errorf("largebody: response facts body bytes must be > 0, got %d", f.BodyBytes)
	}
	if f.RewrittenLength < 0 {
		return fmt.Errorf("largebody: response facts rewritten length must be >= 0, got %d", f.RewrittenLength)
	}
	return nil
}

// SessionResponseCarrier returns new-session BeginTurn response values to
// the frontend: authoritative session ID, A-leg ID, and the raw resume
// token (design section 10, Requirement 14.6). Values are never metrics
// labels/log fields and are released with request lifetime
// (Requirements 14.7, 22.3): every default rendering is redacted and the
// frontend extracts values explicitly for session/resume headers.
type SessionResponseCarrier struct {
	AuthoritativeSessionID string
	ALegID                 string
	ResumeToken            SensitiveString
}

// Validate enforces bounds under the semantic-fact budget.
func (c SessionResponseCarrier) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"authoritative session id": c.AuthoritativeSessionID,
		"a-leg id":                 c.ALegID,
	} {
		if int64(len(v)) > maxFactBytes {
			return fmt.Errorf("largebody: session carrier %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	if int64(len(c.ResumeToken.Reveal())) > maxFactBytes {
		return fmt.Errorf("largebody: session carrier resume token exceeds %d bytes", maxFactBytes)
	}
	return nil
}

// String renders presence signals only; values stay redacted.
func (c SessionResponseCarrier) String() string {
	return fmt.Sprintf("SessionResponseCarrier{has_session_id:%t has_aleg_id:%t has_resume_token:%t}",
		c.AuthoritativeSessionID != "", c.ALegID != "", !c.ResumeToken.IsZero())
}

// GoString renders presence signals only; values stay redacted.
func (c SessionResponseCarrier) GoString() string { return c.String() }

// Format renders presence signals only for every verb.
func (c SessionResponseCarrier) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, c.String())
}

// MarshalJSON emits presence signals only; values stay redacted.
func (c SessionResponseCarrier) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		HasSessionID   bool `json:"has_session_id"`
		HasALegID      bool `json:"has_aleg_id"`
		HasResumeToken bool `json:"has_resume_token"`
	}{
		HasSessionID:   c.AuthoritativeSessionID != "",
		HasALegID:      c.ALegID != "",
		HasResumeToken: !c.ResumeToken.IsZero(),
	})
}

// ExecutionResult is the wire execution output (design section 13):
// the canonical event stream plus bounded response facts. An
// EventStream alone is insufficient (Requirement 18.1).
type ExecutionResult struct {
	Stream  lipapi.EventStream
	Facts   ResponseFacts
	Session SessionResponseCarrier
}

// Validate requires the canonical stream and valid bounded facts.
func (r ExecutionResult) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if r.Stream == nil {
		return fmt.Errorf("largebody: execution result stream must not be nil")
	}
	if err := r.Facts.Validate(maxFactBytes); err != nil {
		return err
	}
	if err := r.Session.Validate(maxFactBytes); err != nil {
		return err
	}
	return nil
}

// DefaultMaxSemanticFactBytes is the default ceiling on profile-derived and
// wire-resolution semantic facts (256 KiB; Requirement 4, design section 3).
const DefaultMaxSemanticFactBytes int64 = 256 * 1024

type factBudgetCtxKey struct{}

// WithSemanticFactBudget attaches a configured semantic-fact budget to ctx.
func WithSemanticFactBudget(ctx context.Context, budget int64) context.Context {
	if budget <= 0 {
		return ctx
	}
	return context.WithValue(ctx, factBudgetCtxKey{}, budget)
}

// SemanticFactBudget returns the configured semantic-fact budget from ctx,
// or DefaultMaxSemanticFactBytes if not set or non-positive.
func SemanticFactBudget(ctx context.Context) int64 {
	if ctx != nil {
		if b, ok := ctx.Value(factBudgetCtxKey{}).(int64); ok && b > 0 {
			return b
		}
	}
	return DefaultMaxSemanticFactBytes
}

// checkBudget rejects a non-positive semantic-fact budget: validation
// without a budget cannot prove boundedness.
func checkBudget(maxFactBytes int64) error {
	if maxFactBytes <= 0 {
		return fmt.Errorf("largebody: fact budget must be > 0, got %d", maxFactBytes)
	}
	return nil
}
