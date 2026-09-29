package sessionclassification

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

type ScopeKind string

const (
	ScopeSecureSession ScopeKind = "secure_session"
	ScopeALeg          ScopeKind = "a_leg"
)

type Key struct {
	Kind ScopeKind
	ID   string
}

func KeyFromSession(v session.SessionView) (Key, error) {
	if id := strings.TrimSpace(v.AuthoritativeSessionID); id != "" {
		return Key{Kind: ScopeSecureSession, ID: id}, nil
	}
	if id := strings.TrimSpace(v.ALegID); id != "" {
		return Key{Kind: ScopeALeg, ID: id}, nil
	}
	return Key{}, fmt.Errorf("%s: no authoritative session or a-leg identity", ID)
}

func (k Key) Validate() error {
	if (k.Kind != ScopeSecureSession && k.Kind != ScopeALeg) || strings.TrimSpace(k.ID) == "" || len(k.ID) > 512 {
		return fmt.Errorf("%s: invalid state key", ID)
	}
	return nil
}

type Record struct {
	Key                  Key
	Classification       session.Classification
	RemoteAttempts       uint32
	RemoteLeaseID        string
	RemoteLeaseUntil     time.Time
	RemoteNextEligibleAt time.Time
	UpdatedAt            time.Time
}

type RemoteClaim struct {
	Key     Key
	LeaseID string
	Attempt uint32
}

type RemoteCompletion struct {
	Proposal session.Classification
}

type State interface {
	Load(context.Context, Key) (Record, bool, error)
	Promote(context.Context, Key, session.Classification, time.Time) (Record, bool, error)
	ClaimRemote(context.Context, Key, time.Time, uint32, time.Duration, time.Duration) (RemoteClaim, Record, bool, error)
	CompleteRemote(context.Context, RemoteClaim, RemoteCompletion, time.Time) (Record, error)
}
