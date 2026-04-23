package credpool

import (
	"fmt"
	"sync"
	"time"
)

// noCopy is a field the same way as in [sync.WaitGroup]: go vet's copylocks
// analyzer reports accidental copies of [Pool] values (which would duplicate [sync.Mutex]).
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// Credential is one API key attached to a backend instance. [New] always
// assigns pool-local opaque ids (c0, c1, …); the ID field on input is ignored.
type Credential struct {
	ID     string // ignored by [New]; acquire returns the assigned pool id
	Secret string
}

// Clock supplies the current time when needed outside explicit Acquire/Snapshot now.
type Clock func() time.Time

// State is usefulness of a credential for diagnostics only.
type State string

const (
	StateUsable      State = "usable"
	StateCooldown    State = "cooldown"
	StateAuthInvalid State = "auth_invalid"
)

// CredentialStatus is a secret-free view for tests and diagnostics.
type CredentialStatus struct {
	ID            string
	State         State
	CooldownUntil time.Time // zero means not in cooldown / n/a for usable
}

// Pool holds ordered credentials and per-credential usefulness state.
type Pool struct {
	noCopy noCopy
	mu     sync.Mutex
	clock  Clock
	creds  []poolEntry
}

type poolEntry struct {
	id            string
	secret        string
	cooldownUntil time.Time // zero = no active cooldown
	isAuthInvalid bool
}

// New builds a pool from normalized credentials (non-empty secrets).
// Each credential receives a stable pool-local id (c0, c1, …) independent of secrets.
func New(credentials []Credential, clock Clock) (*Pool, error) {
	if len(credentials) == 0 {
		return nil, errEmptyCredentialList
	}
	if clock == nil {
		clock = time.Now
	}
	out := make([]poolEntry, 0, len(credentials))
	for i, c := range credentials {
		if c.Secret == "" {
			return nil, fmt.Errorf("credpool: empty secret at index %d", i)
		}
		// Pool-local stable ids; never derive from secret material.
		id := fmt.Sprintf("c%d", i)
		out = append(out, poolEntry{id: id, secret: c.Secret})
	}
	return &Pool{clock: clock, creds: out}, nil
}

// Acquire returns the first usable credential in registration order: not in
// exclude, not auth-invalid, and not in an active cooldown at now.
func (p *Pool) Acquire(now time.Time, exclude map[string]struct{}) (Credential, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.creds {
		e := &p.creds[i]
		if exclude != nil {
			if _, skip := exclude[e.id]; skip {
				continue
			}
		}
		if e.isAuthInvalid {
			continue
		}
		if !e.cooldownUntil.IsZero() && e.cooldownUntil.After(now) {
			continue
		}
		return Credential{ID: e.id, Secret: e.secret}, nil
	}
	return Credential{}, ErrNoUsableCredential
}

// MarkRateLimited puts the credential in cooldown until the given instant.
func (p *Pool) MarkRateLimited(id string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.creds {
		if p.creds[i].id == id {
			p.creds[i].cooldownUntil = until
			return
		}
	}
}

// MarkAuthInvalid permanently marks the credential unusable for auth reasons.
func (p *Pool) MarkAuthInvalid(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.creds {
		if p.creds[i].id == id {
			p.creds[i].isAuthInvalid = true
			return
		}
	}
}

// Snapshot returns secret-free status for every credential in order.
func (p *Pool) Snapshot(now time.Time) []CredentialStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]CredentialStatus, 0, len(p.creds))
	for i := range p.creds {
		e := p.creds[i]
		st := CredentialStatus{ID: e.id}
		switch {
		case e.isAuthInvalid:
			st.State = StateAuthInvalid
		case !e.cooldownUntil.IsZero() && e.cooldownUntil.After(now):
			st.State = StateCooldown
			st.CooldownUntil = e.cooldownUntil
		default:
			st.State = StateUsable
		}
		out = append(out, st)
	}
	return out
}
