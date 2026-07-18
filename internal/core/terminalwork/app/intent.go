package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

const intentIdentityVersion = 1

// IntentStore persists durable terminal-work intents before promotion.
type IntentStore interface {
	AppendIntent(ctx context.Context, rec terminalwork.WorkRecord) error
	PromotePending(ctx context.Context, cmd terminalwork.PromotePendingCommand) error
}

// IntentServiceConfig configures IntentService clocks.
type IntentServiceConfig struct {
	Clock func() time.Time
}

// IntentService accepts durable settle/release intents with privacy-safe rows
// (requirements 8.3, 8.7–8.9, 12.8; design D9, D14).
type IntentService struct {
	store IntentStore
	clock func() time.Time
}

// NewIntentService returns an intent accepter backed by store.
func NewIntentService(store IntentStore, cfg IntentServiceConfig) *IntentService {
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &IntentService{store: store, clock: clock}
}

// SettleFailureInput describes one failed provider settle for durable recovery.
type SettleFailureInput struct {
	RequestID  string
	AttemptID  string
	TraceID    string
	ProviderID string
	Handles    []string
	Versions   terminalwork.BoundVersions
}

// ReleaseFailureInput describes one failed provider release for durable recovery.
type ReleaseFailureInput struct {
	RequestID  string
	AttemptID  string
	TraceID    string
	ProviderID string
	Handle     string
	Versions   terminalwork.BoundVersions
}

// AcceptSettleFailure appends and promotes a settle-request-provider intent.
// Raw cause text is never persisted (design D14). WorkID/SourceKey are hash-based
// and include AttemptID so repeated request actions do not collide. Payload
// handles are sorted identically to the identity material for SameIntentReplay.
func (s *IntentService) AcceptSettleFailure(ctx context.Context, in SettleFailureInput) error {
	if s == nil || s.store == nil {
		return ErrNilIntentStore
	}
	providerID := strings.TrimSpace(in.ProviderID)
	requestID := strings.TrimSpace(in.RequestID)
	if providerID == "" || requestID == "" {
		return fmt.Errorf("%w: settle intent identity", sdk.ErrInvalid)
	}
	handles := cleanHandles(in.Handles)
	attemptID := strings.TrimSpace(in.AttemptID)
	traceID := strings.TrimSpace(in.TraceID)
	workID, sourceKey := durableWorkIdentity(sdk.WorkKindSettleRequestProvider, requestID, attemptID, providerID, handles)
	payload, err := safeHandlesPayload(handles)
	if err != nil {
		return err
	}
	versions := in.Versions
	if strings.TrimSpace(versions.ProviderID) == "" {
		versions.ProviderID = providerID
	}
	rec := terminalwork.WorkRecord{
		WorkID:         workID,
		SourceKey:      sourceKey,
		PayloadVersion: 1,
		Kind:           sdk.WorkKindSettleRequestProvider,
		State:          sdk.WorkStateIntent,
		ProviderID:     providerID,
		Lifecycle: terminalwork.LifecycleCorrelation{
			RequestID: requestID,
			AttemptID: attemptID,
			TraceID:   traceID,
		},
		Versions: versions,
		Payload:  payload,
		Error: terminalwork.BoundedError{
			Code:    "outage",
			Message: "provider settle failed",
		},
	}
	return s.accept(ctx, rec)
}

// AcceptReleaseFailure appends and promotes a release-request-provider intent.
func (s *IntentService) AcceptReleaseFailure(ctx context.Context, in ReleaseFailureInput) error {
	if s == nil || s.store == nil {
		return ErrNilIntentStore
	}
	providerID := strings.TrimSpace(in.ProviderID)
	requestID := strings.TrimSpace(in.RequestID)
	if providerID == "" || requestID == "" {
		return fmt.Errorf("%w: release intent identity", sdk.ErrInvalid)
	}
	handles := cleanHandles([]string{in.Handle})
	attemptID := strings.TrimSpace(in.AttemptID)
	traceID := strings.TrimSpace(in.TraceID)
	workID, sourceKey := durableWorkIdentity(sdk.WorkKindReleaseRequestProvider, requestID, attemptID, providerID, handles)
	payload, err := safeHandlesPayload(handles)
	if err != nil {
		return err
	}
	versions := in.Versions
	if strings.TrimSpace(versions.ProviderID) == "" {
		versions.ProviderID = providerID
	}
	rec := terminalwork.WorkRecord{
		WorkID:         workID,
		SourceKey:      sourceKey,
		PayloadVersion: 1,
		Kind:           sdk.WorkKindReleaseRequestProvider,
		State:          sdk.WorkStateIntent,
		ProviderID:     providerID,
		Lifecycle: terminalwork.LifecycleCorrelation{
			RequestID: requestID,
			AttemptID: attemptID,
			TraceID:   traceID,
		},
		Versions: versions,
		Payload:  payload,
		Error: terminalwork.BoundedError{
			Code:    "outage",
			Message: "provider release failed",
		},
	}
	return s.accept(ctx, rec)
}

func (s *IntentService) accept(ctx context.Context, rec terminalwork.WorkRecord) error {
	if ctx == nil {
		ctx = context.Background()
	}
	now := s.clock().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	if err := s.store.AppendIntent(ctx, rec); err != nil {
		return err
	}
	return s.store.PromotePending(ctx, terminalwork.PromotePendingCommand{
		WorkID: rec.WorkID,
		Now:    now,
	})
}

func durableWorkIdentity(kind sdk.WorkKind, requestID, attemptID, providerID string, handles []string) (string, terminalwork.SourceKey) {
	var b strings.Builder
	writeLenPrefixed(&b, string(kind))
	writeLenPrefixed(&b, requestID)
	writeLenPrefixed(&b, attemptID)
	writeLenPrefixed(&b, providerID)
	sorted := append([]string(nil), handles...)
	sort.Strings(sorted)
	for _, h := range sorted {
		writeLenPrefixed(&b, h)
	}
	sum := sha256.Sum256([]byte(b.String()))
	digest := hex.EncodeToString(sum[:16])
	return "tw_" + digest, terminalwork.SourceKey{
		IdentityVersion: intentIdentityVersion,
		Key:             "sk_" + digest,
	}
}

func writeLenPrefixed(b *strings.Builder, s string) {
	s = strings.TrimSpace(s)
	fmt.Fprintf(b, "%d:%s|", len(s), s)
}

func cleanHandles(handles []string) []string {
	clean := make([]string, 0, len(handles))
	seen := make(map[string]struct{}, len(handles))
	for _, h := range handles {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		clean = append(clean, h)
	}
	sort.Strings(clean)
	return clean
}

func safeHandlesPayload(handles []string) ([]byte, error) {
	// handles are already sorted by cleanHandles; keep payload identical to identity.
	return json.Marshal(struct {
		Handles []string `json:"handles,omitempty"`
	}{Handles: handles})
}
