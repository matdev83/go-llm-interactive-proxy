// Package sessionwire holds shared LIP session carrier keys and decode helpers for frontends.
// Security decisions stay in core; adapters only lift protocol-legal fields into [lipapi.SessionRef].
package sessionwire

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// LIP-controlled HTTP carriers for secure-session resume (proxy-issued values round-tripped by clients).
const (
	HeaderAuthoritativeSessionID = lipsdk.HeaderSessionID
	HeaderResumeToken            = lipsdk.HeaderResumeToken
	HeaderALegID                 = lipsdk.HeaderALegID
	HeaderSessionHint            = lipsdk.HeaderSessionHint
	// Metadata keys for JSON request bodies (OpenAI-style metadata maps).
	MetaKeyAuthoritativeSessionID = "lip_session_id"
	MetaKeyResumeToken            = "lip_resume_token"
)

// HTTPStatusForSessionDenial maps stable [lipapi.SessionDenialCode] values to HTTP status codes
// for frontend adapters (protocol-neutral transport semantics).
func HTTPStatusForSessionDenial(code lipapi.SessionDenialCode) int {
	switch code {
	case lipapi.SessionDeniedMissingPrincipal:
		return 401
	case lipapi.SessionDeniedStorageUnavailable,
		lipapi.SessionDeniedMandatoryAuditFailure,
		lipapi.SessionDeniedPolicyUnavailable:
		return 503
	default:
		return 400
	}
}

// ApplyAuthoritativeHeaders merges LIP session headers into ref when present (trimmed).
// Caller supplies ref; zero fields are left unchanged when a header is absent.
func ApplyAuthoritativeHeaders(ref *lipapi.SessionRef, h http.Header) {
	ApplyAuthoritativeHeadersNamed(ref, h, nil, nil)
}

// ApplyAuthoritativeHeadersNamed merges session-id and resume-token from the given
// header names. Empty name lists use [HeaderAuthoritativeSessionID] and [HeaderResumeToken].
func ApplyAuthoritativeHeadersNamed(ref *lipapi.SessionRef, h http.Header, sessionID, resumeToken []string) {
	if ref == nil || h == nil {
		return
	}
	if len(sessionID) == 0 {
		sessionID = []string{HeaderAuthoritativeSessionID}
	}
	if len(resumeToken) == 0 {
		resumeToken = []string{HeaderResumeToken}
	}
	if v := firstHeader(h, sessionID); v != "" {
		ref.AuthoritativeSessionID = v
	}
	if v := firstHeader(h, resumeToken); v != "" {
		ref.ResumeToken = v
	}
}

func firstHeader(h http.Header, names []string) string {
	return lipsdk.FirstHeader(h, names)
}

// ApplyMetadata merges string metadata entries into ref when keys are present (trimmed).
// Headers should be applied after metadata if headers must win on conflict.
func ApplyMetadata(ref *lipapi.SessionRef, meta map[string]string) {
	if ref == nil || len(meta) == 0 {
		return
	}
	if v := strings.TrimSpace(meta[MetaKeyAuthoritativeSessionID]); v != "" {
		ref.AuthoritativeSessionID = v
	}
	if v := strings.TrimSpace(meta[MetaKeyResumeToken]); v != "" {
		ref.ResumeToken = v
	}
}

// ValidateMetadata checks LIP-controlled session metadata carriers before callers copy
// them into the canonical session reference.
func ValidateMetadata(meta map[string]string) error {
	if len(meta) == 0 {
		return nil
	}
	if err := validateMetadataCarrier(MetaKeyAuthoritativeSessionID, meta[MetaKeyAuthoritativeSessionID], lipapi.MaxAuthoritativeSessionIDBytes); err != nil {
		return err
	}
	if err := validateMetadataCarrier(MetaKeyResumeToken, meta[MetaKeyResumeToken], lipapi.MaxResumeTokenBytes); err != nil {
		return err
	}
	return nil
}

func validateMetadataCarrier(key, value string, maxBytes int) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) <= maxBytes {
		return nil
	}
	return fmt.Errorf("metadata %s exceeds %d bytes", key, maxBytes)
}

// WithoutSensitiveToken replaces exact rawToken substrings in s for log-safe strings.
func WithoutSensitiveToken(s, rawToken string) string {
	if s == "" || rawToken == "" {
		return s
	}
	return strings.ReplaceAll(s, rawToken, "[REDACTED]")
}

// WriteResponseCarriers sets LIP session response headers from call.Session when fields are non-empty.
func WriteResponseCarriers(w http.ResponseWriter, call *lipapi.Call) {
	if w == nil || call == nil {
		return
	}
	if sid := strings.TrimSpace(call.Session.AuthoritativeSessionID); sid != "" {
		w.Header().Set(HeaderAuthoritativeSessionID, sid)
	}
	if tok := strings.TrimSpace(call.Session.ResumeToken); tok != "" {
		w.Header().Set(HeaderResumeToken, tok)
	}
	if aLegID := strings.TrimSpace(call.Session.ALegID); aLegID != "" {
		w.Header().Set(HeaderALegID, aLegID)
	}
}
