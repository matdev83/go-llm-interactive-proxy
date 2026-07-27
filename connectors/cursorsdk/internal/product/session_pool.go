package product

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
)

var (
	ErrAgentBusy      = errors.New("cursor_sdk_agent_busy")
	ErrAgentLimit     = errors.New("cursor_sdk_agent_limit")
	ErrRunLimit       = errors.New("cursor_sdk_run_limit")
	ErrPoolClosed     = errors.New("cursorsdk: session pool closed")
	ErrEmptyPrompt    = errors.New("cursor_sdk_prompt_empty")
	ErrCommitRequired = errors.New("cursor_sdk_commit_required")
)

type InvalidationCause string

const (
	InvalidateTranscript  InvalidationCause = "transcript"
	InvalidateIdentity    InvalidationCause = "identity"
	InvalidateCancel      InvalidationCause = "cancel"
	InvalidateRunError    InvalidationCause = "run_error"
	InvalidateBridge      InvalidationCause = "bridge"
	InvalidateEvict       InvalidationCause = "evict"
	InvalidateShutdown    InvalidationCause = "shutdown"
	InvalidateGeneration  InvalidationCause = "generation"
	InvalidateCreateFail  InvalidationCause = "create_failed"
	InvalidateSendFail    InvalidationCause = "send_failed"
	InvalidateUncommitted InvalidationCause = "uncommitted"
)

type AgentLease struct {
	Key           AgentKey
	AgentID       string
	RunID         string
	Generation    int64 // bridge process generation at PrepareSend
	Mode          HistoryMode
	PendingMarker HistoryMarker
	leaseSeq      uint64
}

// ProcessGeneration returns the bridge process generation bound to this lease.
func (l *AgentLease) ProcessGeneration() int64 {
	if l == nil {
		return 0
	}
	return l.Generation
}

type PrepareSendInput struct {
	Key          AgentKey
	Create       protocol.AgentCreateParams
	View         TranscriptView
	FullPrompt   string
	SuffixPrompt string
}

type SessionPoolOpts struct {
	Now            func() time.Time
	DisposeTimeout time.Duration
	Diag           *Diag
}

type agentEntry struct {
	key           AgentKey
	agentID       string
	generation    int64
	marker        HistoryMarker
	busy          bool
	creating      bool
	pendingCommit bool
	lastUsed      time.Time
	leaseSeq      uint64
}

type SessionPool struct {
	cfg            Config
	bridge         AgentBridge
	now            func() time.Time
	disposeTimeout time.Duration
	diag           *Diag

	mu        sync.Mutex
	entries   map[string]*agentEntry
	bySession map[string]string
	order     []string
	closed    bool
	leaseN    atomic.Uint64
}

func NewSessionPool(cfg Config, bridge AgentBridge, opts SessionPoolOpts) *SessionPool {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	disposeTO := opts.DisposeTimeout
	if disposeTO <= 0 {
		disposeTO = cfg.ShutdownTimeout
	}
	if disposeTO <= 0 {
		disposeTO = 5 * time.Second
	}
	return &SessionPool{
		cfg:            cfg,
		bridge:         bridge,
		now:            now,
		disposeTimeout: disposeTO,
		diag:           opts.Diag,
		entries:        make(map[string]*agentEntry),
		bySession:      make(map[string]string),
	}
}

func (p *SessionPool) LiveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

func (p *SessionPool) BusyCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.busyCountLocked()
}

func (p *SessionPool) Marker(key AgentKey) HistoryMarker {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.entries[key.IdentityHash()]; e != nil {
		return e.marker
	}
	return HistoryMarker{}
}

func (p *SessionPool) PrepareSend(ctx context.Context, in PrepareSendInput) (*AgentLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.ReapIdle()

	info, err := p.bridge.EnsureReady(ctx)
	if err != nil {
		return nil, err
	}
	gen := info.Generation
	if gen == 0 {
		gen = p.bridge.Generation()
	}

	idHash := in.Key.IdentityHash()
	var stale []*agentEntry

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrPoolClosed
	}
	stale = append(stale, p.takeStaleGenerationLocked(gen)...)
	stale = append(stale, p.takeSupersededSessionLocked(in.Key)...)

	entry := p.entries[idHash]
	if entry != nil && entry.pendingCommit {
		stale = append(stale, entry)
		p.removeEntryLocked(idHash)
		entry = nil
	}
	if entry != nil && (entry.busy || entry.creating) {
		p.mu.Unlock()
		p.disposeAll(stale)
		return nil, ErrAgentBusy
	}

	committed := HistoryMarker{}
	if entry != nil {
		committed = entry.marker
	}
	plan := PlanHistory(in.View, committed, in.Key, gen)

	if entry != nil && (plan.ResetNeeded || entry.generation != gen) {
		stale = append(stale, entry)
		p.removeEntryLocked(idHash)
		entry = nil
		plan = PlanHistory(in.View, HistoryMarker{}, in.Key, gen)
	}

	if err := validatePrompts(plan, in); err != nil {
		p.mu.Unlock()
		p.disposeAll(stale)
		return nil, err
	}

	needCreate := entry == nil
	if !needCreate && p.busyCountLocked() >= p.cfg.MaxConcurrentRuns {
		p.mu.Unlock()
		p.disposeAll(stale)
		return nil, ErrRunLimit
	}
	if needCreate {
		if p.busyCountLocked() >= p.cfg.MaxConcurrentRuns {
			p.mu.Unlock()
			p.disposeAll(stale)
			return nil, ErrRunLimit
		}
		for len(p.entries) >= p.cfg.MaxAgents {
			evicted := p.evictOneIdleLocked()
			if evicted == nil {
				p.mu.Unlock()
				p.disposeAll(stale)
				return nil, ErrAgentLimit
			}
			stale = append(stale, evicted)
		}
		entry = &agentEntry{
			key:        in.Key,
			generation: gen,
			creating:   true,
			lastUsed:   p.now(),
		}
		p.entries[idHash] = entry
		p.bySession[in.Key.SessionID] = idHash
		p.touchOrderLocked(idHash)
	} else {
		entry.busy = true
		entry.lastUsed = p.now()
		p.touchOrderLocked(idHash)
	}
	existingID := entry.agentID
	p.mu.Unlock()

	evictedN := len(stale)
	p.disposeAll(stale)
	if evictedN > 0 && p.diag != nil {
		p.diag.LogPool(ctx, "evict", InvalidateEvict, p.LiveCount(), p.BusyCount(), DiagCorr{})
	}

	prompt := in.SuffixPrompt
	if plan.UseFullPrompt || plan.Mode == HistoryBootstrap {
		prompt = in.FullPrompt
	}
	if plan.Mode == HistoryRetry {
		prompt = in.SuffixPrompt
	}

	agentID := existingID
	outcome := "reuse"
	if needCreate {
		outcome = "create"
		createParams := in.Create
		createParams.EnableAgentRetries = false
		id, err := p.bridge.CreateAgent(ctx, createParams)
		if err != nil {
			p.failCreate(idHash)
			p.emitClassifiedPoolRun(ctx, "create_failed", InvalidateCreateFail, err, in.Create.APIKey)
			return nil, fmt.Errorf("cursorsdk: agent create failed: %w", redactSecret(err, in.Create.APIKey))
		}
		agentID = id
		p.mu.Lock()
		if e := p.entries[idHash]; e != nil {
			e.agentID = agentID
			e.creating = false
			e.busy = true
			e.generation = gen
			e.lastUsed = p.now()
		} else {
			p.mu.Unlock()
			p.disposeBestEffort(agentID)
			return nil, ErrPoolClosed
		}
		p.mu.Unlock()
	}
	if p.diag != nil {
		p.diag.LogPool(ctx, outcome, "", p.LiveCount(), p.BusyCount(), DiagCorr{})
	}

	runID, err := p.bridge.SendAgent(ctx, agentID, prompt)
	if err != nil {
		p.failSend(idHash, agentID)
		p.emitClassifiedPoolRun(ctx, "send_failed", InvalidateSendFail, err, in.Create.APIKey)
		return nil, fmt.Errorf("cursorsdk: agent send failed: %w", ClassifyAndMap(err, false, in.Create.APIKey))
	}

	leaseSeq := p.leaseN.Add(1)
	p.mu.Lock()
	if e := p.entries[idHash]; e != nil {
		e.busy = true
		e.pendingCommit = true
		e.leaseSeq = leaseSeq
		e.lastUsed = p.now()
	}
	p.mu.Unlock()

	return &AgentLease{
		Key:           in.Key,
		AgentID:       agentID,
		RunID:         runID,
		Generation:    gen,
		Mode:          plan.Mode,
		PendingMarker: plan.NextMarker,
		leaseSeq:      leaseSeq,
	}, nil
}

func validatePrompts(plan HistoryPlan, in PrepareSendInput) error {
	if plan.UseFullPrompt || plan.Mode == HistoryBootstrap {
		if strings.TrimSpace(in.FullPrompt) == "" {
			return fmt.Errorf("%w: bootstrap/full prompt required", ErrEmptyPrompt)
		}
		return nil
	}
	if strings.TrimSpace(in.SuffixPrompt) == "" {
		return fmt.Errorf("%w: incremental/retry suffix prompt required", ErrEmptyPrompt)
	}
	return nil
}

func redactSecret(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, secret) {
		return err
	}
	return errors.New(strings.ReplaceAll(msg, secret, "[REDACTED]"))
}

func (p *SessionPool) CommitSend(lease *AgentLease) {
	if lease == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[lease.Key.IdentityHash()]
	if e == nil || e.leaseSeq != lease.leaseSeq || e.agentID != lease.AgentID {
		return
	}
	if !e.pendingCommit {
		return
	}
	e.marker = lease.PendingMarker
	e.pendingCommit = false
}

func (p *SessionPool) ReleaseReady(lease *AgentLease) error {
	if lease == nil {
		return nil
	}
	p.mu.Lock()
	e := p.entries[lease.Key.IdentityHash()]
	if e == nil || e.leaseSeq != lease.leaseSeq {
		p.mu.Unlock()
		return nil
	}
	if e.pendingCommit {
		p.removeEntryLocked(lease.Key.IdentityHash())
		agentID := e.agentID
		p.mu.Unlock()
		p.disposeBestEffort(agentID)
		return ErrCommitRequired
	}
	e.busy = false
	e.lastUsed = p.now()
	p.touchOrderLocked(lease.Key.IdentityHash())
	p.mu.Unlock()
	return nil
}

func (p *SessionPool) InvalidateLease(lease *AgentLease, cause InvalidationCause) {
	if lease == nil {
		return
	}
	p.mu.Lock()
	e := p.entries[lease.Key.IdentityHash()]
	if e == nil || e.leaseSeq != lease.leaseSeq {
		p.mu.Unlock()
		return
	}
	p.removeEntryLocked(lease.Key.IdentityHash())
	agentID := e.agentID
	live, busy := len(p.entries), p.busyCountLocked()
	p.mu.Unlock()
	p.disposeBestEffort(agentID)
	if p.diag != nil {
		p.diag.LogPool(context.Background(), "invalidate", cause, live, busy, DiagCorr{})
	}
}

func (p *SessionPool) InvalidateKey(key AgentKey, cause InvalidationCause) {
	p.mu.Lock()
	e := p.entries[key.IdentityHash()]
	if e == nil {
		p.mu.Unlock()
		return
	}
	p.removeEntryLocked(key.IdentityHash())
	agentID := e.agentID
	live, busy := len(p.entries), p.busyCountLocked()
	p.mu.Unlock()
	p.disposeBestEffort(agentID)
	if p.diag != nil {
		p.diag.LogPool(context.Background(), "invalidate", cause, live, busy, DiagCorr{})
	}
}

func (p *SessionPool) InvalidateAll(cause InvalidationCause) {
	p.mu.Lock()
	snapshot := make([]*agentEntry, 0, len(p.entries))
	for id, e := range p.entries {
		snapshot = append(snapshot, e)
		delete(p.entries, id)
	}
	p.bySession = make(map[string]string)
	p.order = nil
	p.mu.Unlock()
	p.disposeAll(snapshot)
	if p.diag != nil {
		p.diag.LogPool(context.Background(), "invalidate", cause, 0, 0, DiagCorr{})
	}
}

func (p *SessionPool) InvalidateGeneration(gen int64, cause InvalidationCause) {
	if p == nil || gen <= 0 {
		return
	}
	p.mu.Lock()
	snapshot := make([]*agentEntry, 0)
	for id, e := range p.entries {
		if e.generation != gen {
			continue
		}
		snapshot = append(snapshot, e)
		p.removeEntryLocked(id)
	}
	live, busy := len(p.entries), p.busyCountLocked()
	p.mu.Unlock()
	p.disposeAll(snapshot)
	if len(snapshot) > 0 && p.diag != nil {
		p.diag.LogPool(context.Background(), "invalidate", cause, live, busy, DiagCorr{})
	}
}

func (p *SessionPool) ReapIdle() {
	p.mu.Lock()
	if p.closed || p.cfg.AgentIdleTimeout <= 0 {
		p.mu.Unlock()
		return
	}
	cutoff := p.now().Add(-p.cfg.AgentIdleTimeout)
	var victims []*agentEntry
	for id, e := range p.entries {
		if e.busy || e.creating || e.pendingCommit {
			continue
		}
		if !e.lastUsed.After(cutoff) {
			victims = append(victims, e)
			p.removeEntryLocked(id)
		}
	}
	n := len(victims)
	p.mu.Unlock()
	p.disposeAll(victims)
	if n > 0 && p.diag != nil {
		p.diag.LogPool(context.Background(), "evict", InvalidateEvict, p.LiveCount(), p.BusyCount(), DiagCorr{})
	}
}

func (p *SessionPool) Close(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	snapshot := make([]*agentEntry, 0, len(p.entries))
	for id, e := range p.entries {
		snapshot = append(snapshot, e)
		delete(p.entries, id)
	}
	p.bySession = make(map[string]string)
	p.order = nil
	p.mu.Unlock()
	var errs []error
	for _, e := range snapshot {
		if err := p.disposeWithContext(ctx, e.agentID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *SessionPool) busyCountLocked() int {
	n := 0
	for _, e := range p.entries {
		if e.busy || e.creating {
			n++
		}
	}
	return n
}

func (p *SessionPool) takeStaleGenerationLocked(gen int64) []*agentEntry {
	var stale []*agentEntry
	for id, e := range p.entries {
		if e.generation != gen {
			stale = append(stale, e)
			p.removeEntryLocked(id)
		}
	}
	return stale
}

func (p *SessionPool) takeSupersededSessionLocked(key AgentKey) []*agentEntry {
	prevHash, ok := p.bySession[key.SessionID]
	if !ok {
		return nil
	}
	idHash := key.IdentityHash()
	if prevHash == idHash {
		return nil
	}
	e := p.entries[prevHash]
	if e == nil {
		delete(p.bySession, key.SessionID)
		return nil
	}
	p.removeEntryLocked(prevHash)
	return []*agentEntry{e}
}

func (p *SessionPool) removeEntryLocked(idHash string) {
	e := p.entries[idHash]
	delete(p.entries, idHash)
	p.removeOrderLocked(idHash)
	if e != nil && p.bySession[e.key.SessionID] == idHash {
		delete(p.bySession, e.key.SessionID)
	}
}

func (p *SessionPool) evictOneIdleLocked() *agentEntry {
	for _, id := range p.order {
		e := p.entries[id]
		if e == nil || e.busy || e.creating || e.pendingCommit {
			continue
		}
		p.removeEntryLocked(id)
		return e
	}
	return nil
}

func (p *SessionPool) touchOrderLocked(id string) {
	p.removeOrderLocked(id)
	p.order = append(p.order, id)
}

func (p *SessionPool) removeOrderLocked(id string) {
	if len(p.order) == 0 {
		return
	}
	out := p.order[:0]
	for _, cur := range p.order {
		if cur != id {
			out = append(out, cur)
		}
	}
	p.order = out
}

func (p *SessionPool) failCreate(idHash string) {
	p.mu.Lock()
	e := p.entries[idHash]
	p.removeEntryLocked(idHash)
	p.mu.Unlock()
	if e != nil {
		p.disposeBestEffort(e.agentID)
	}
}

func (p *SessionPool) failSend(idHash, agentID string) {
	p.mu.Lock()
	p.removeEntryLocked(idHash)
	p.mu.Unlock()
	p.disposeBestEffort(agentID)
}

// emitClassifiedPoolRun records pool+run failure diagnostics after unlock.
// ClassifyFailure supplies code/phase; slog never runs while p.mu is held.
func (p *SessionPool) emitClassifiedPoolRun(ctx context.Context, outcome string, cause InvalidationCause, err error, apiKey string) {
	if p == nil || p.diag == nil || err == nil {
		return
	}
	cf := ClassifyFailure(err, false, apiKey)
	code, phase := CodeRunFailed, "pre_output"
	if cf != nil {
		code, phase = cf.Code, string(cf.Phase)
	}
	live, busy := p.LiveCount(), p.BusyCount()
	p.diag.LogPoolClassified(ctx, outcome, cause, code, phase, live, busy, DiagCorr{})
	p.diag.LogRun(ctx, "error", phase, code, "", DiagCorr{})
}

func (p *SessionPool) disposeAll(entries []*agentEntry) {
	for _, e := range entries {
		if e != nil {
			p.disposeBestEffort(e.agentID)
		}
	}
}

func (p *SessionPool) disposeBestEffort(agentID string) {
	if agentID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.disposeTimeout)
	defer cancel()
	_ = p.bridge.DisposeAgent(ctx, agentID)
}

func (p *SessionPool) disposeWithContext(ctx context.Context, agentID string) error {
	if agentID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dctx, cancel := context.WithTimeout(ctx, p.disposeTimeout)
	defer cancel()
	return p.bridge.DisposeAgent(dctx, agentID)
}
