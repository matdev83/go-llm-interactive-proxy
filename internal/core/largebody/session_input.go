package largebody

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ErrBodySessionMetadataRejected indicates that the profile rejected body-carried
// LIP session metadata (Requirement 14.2, Requirement 17.5).
var ErrBodySessionMetadataRejected = errors.New("largebody: body-carried LIP session metadata rejected by profile")

// SessionInputSource carries raw candidate session values from inbound HTTP headers
// and body metadata before precedence and bounds enforcement.
type SessionInputSource struct {
	HeaderAuthoritativeSessionID string
	HeaderResumeToken            string
	HeaderALegID                 string
	HeaderClientSessionHint      string

	BodyAuthoritativeSessionID string
	BodyResumeToken            string
	HasBodySessionMetadata     bool

	RejectBodyMetadata bool
}

// BuildSessionInput builds an exact bounded SessionInput from raw source inputs.
// It enforces:
//   - Header-over-body precedence for authoritative session ID and resume token (Requirement 14.1);
//   - Rejection of body-carried LIP session metadata when RejectBodyMetadata is true (Requirements 14.2, 17.5);
//   - Bounded sizes under lipapi envelope caps and semantic-fact budget;
//   - SensitiveString wrapping for resume tokens so they never leak into telemetry or backends (Requirements 14.1, 22);
//   - Accurate NewSessionRequested determination (true when both authoritative session ID and resume token are empty).
func BuildSessionInput(src SessionInputSource, maxFactBytes int64) (SessionInput, error) {
	if err := checkBudget(maxFactBytes); err != nil {
		return SessionInput{}, err
	}

	if src.RejectBodyMetadata {
		if src.HasBodySessionMetadata || strings.TrimSpace(src.BodyAuthoritativeSessionID) != "" || strings.TrimSpace(src.BodyResumeToken) != "" {
			return SessionInput{}, fmt.Errorf("%w: request contains body-carried session metadata", ErrBodySessionMetadataRejected)
		}
	}

	authSessionID := strings.TrimSpace(src.BodyAuthoritativeSessionID)
	if hdr := strings.TrimSpace(src.HeaderAuthoritativeSessionID); hdr != "" {
		authSessionID = hdr
	}

	resumeToken := strings.TrimSpace(src.BodyResumeToken)
	if hdr := strings.TrimSpace(src.HeaderResumeToken); hdr != "" {
		resumeToken = hdr
	}

	aLegID := strings.TrimSpace(src.HeaderALegID)
	clientSessionID := strings.TrimSpace(src.HeaderClientSessionHint)

	if len(authSessionID) > lipapi.MaxAuthoritativeSessionIDBytes {
		return SessionInput{}, fmt.Errorf("largebody: session authoritative session id exceeds %d bytes", lipapi.MaxAuthoritativeSessionIDBytes)
	}
	if len(resumeToken) > lipapi.MaxResumeTokenBytes {
		return SessionInput{}, fmt.Errorf("largebody: session resume token exceeds %d bytes", lipapi.MaxResumeTokenBytes)
	}
	if len(aLegID) > lipapi.MaxALegIDBytes {
		return SessionInput{}, fmt.Errorf("largebody: session a-leg id exceeds %d bytes", lipapi.MaxALegIDBytes)
	}
	if len(clientSessionID) > lipapi.MaxClientSessionIDBytes {
		return SessionInput{}, fmt.Errorf("largebody: session client session id exceeds %d bytes", lipapi.MaxClientSessionIDBytes)
	}

	newSession := (authSessionID == "" && resumeToken == "")

	res := SessionInput{
		AuthoritativeSessionID: authSessionID,
		ClientSessionID:        clientSessionID,
		ALegID:                 aLegID,
		ResumeToken:            NewSensitiveString(resumeToken),
		NewSessionRequested:    newSession,
	}

	if err := res.Validate(maxFactBytes); err != nil {
		return SessionInput{}, err
	}

	return res, nil
}

// CorrelationID returns a stable identifier for diagnostics and traffic capture:
// authoritative session ID when set, otherwise client session ID.
func (s SessionInput) CorrelationID() string {
	if x := strings.TrimSpace(s.AuthoritativeSessionID); x != "" {
		return x
	}
	return strings.TrimSpace(s.ClientSessionID)
}

// String implements fmt.Stringer to ensure resume tokens are never formatted.
func (s SessionInput) String() string {
	token := ""
	if !s.ResumeToken.IsZero() {
		token = redactedSensitive
	}
	return fmt.Sprintf("SessionInput{AuthoritativeSessionID:%s ClientSessionID:%s ALegID:%s ResumeToken:%s NewSessionRequested:%t}",
		s.AuthoritativeSessionID, s.ClientSessionID, s.ALegID, token, s.NewSessionRequested)
}

// GoString implements fmt.GoStringer.
func (s SessionInput) GoString() string {
	return s.String()
}

// Format ensures that even under custom fmt verbs, the resume token is never printed.
func (s SessionInput) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, s.String())
}

// LogValue implements slog.LogValuer to ensure resume tokens are never logged.
func (s SessionInput) LogValue() slog.Value {
	token := ""
	if !s.ResumeToken.IsZero() {
		token = redactedSensitive
	}
	return slog.GroupValue(
		slog.String("authoritative_session_id", s.AuthoritativeSessionID),
		slog.String("client_session_id", s.ClientSessionID),
		slog.String("aleg_id", s.ALegID),
		slog.String("resume_token", token),
		slog.Bool("new_session_requested", s.NewSessionRequested),
	)
}

// SessionRef converts the bounded SessionInput into a canonical lipapi.SessionRef.
func (s SessionInput) SessionRef() lipapi.SessionRef {
	return lipapi.SessionRef{
		AuthoritativeSessionID: s.AuthoritativeSessionID,
		ClientSessionID:        s.ClientSessionID,
		ALegID:                 s.ALegID,
		ResumeToken:            s.ResumeToken.Reveal(),
	}
}

// SessionInputFromRef constructs a SessionInput from a canonical lipapi.SessionRef.
func SessionInputFromRef(ref lipapi.SessionRef) SessionInput {
	authID := strings.TrimSpace(ref.AuthoritativeSessionID)
	resume := strings.TrimSpace(ref.ResumeToken)
	return SessionInput{
		AuthoritativeSessionID: authID,
		ClientSessionID:        strings.TrimSpace(ref.ClientSessionID),
		ALegID:                 strings.TrimSpace(ref.ALegID),
		ResumeToken:            NewSensitiveString(resume),
		NewSessionRequested:    authID == "" && resume == "",
	}
}
