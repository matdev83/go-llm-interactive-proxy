package journalstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// MemoryConfig configures an in-memory metering journal.
type MemoryConfig struct {
	StoreID         string
	DefaultPageSize int
	MaxPageSize     int
	Now             func() time.Time
}

// MemoryStore is a concurrent in-memory metering journal.
type MemoryStore struct {
	cfg             MemoryConfig
	defaultPageSize int
	maxPageSize     int
	now             func() time.Time

	mu       sync.Mutex
	seq      int64
	bySource map[string]int // source_event_key -> index in facts
	facts    []storedFact
}

type storedFact struct {
	seq    int64
	source string
	fact   metering.Fact
}

// NewMemoryStore returns a ready in-memory journal.
func NewMemoryStore(cfg MemoryConfig) (*MemoryStore, error) {
	if strings.TrimSpace(cfg.StoreID) == "" {
		return nil, fmt.Errorf("metering/journalstore: memory store id is required")
	}
	def := cfg.DefaultPageSize
	if def <= 0 {
		def = 100
	}
	max := cfg.MaxPageSize
	if max <= 0 {
		max = 500
	}
	if max < def {
		return nil, fmt.Errorf("metering/journalstore: max page size %d < default %d", max, def)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		cfg:             cfg,
		defaultPageSize: def,
		maxPageSize:     max,
		now:             now,
		bySource:        make(map[string]int),
	}, nil
}

// Close is a no-op for the memory store.
func (s *MemoryStore) Close() error { return nil }

// CheckReadiness always succeeds for memory.
func (s *MemoryStore) CheckReadiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// Append validates and stores one fact with source-key idempotency.
// SameFactReplay → no-op; same source key otherwise → ErrIdentityCollision.
func (s *MemoryStore) Append(ctx context.Context, fact metering.Fact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := fact.Validate(); err != nil {
		return fmt.Errorf("metering/journalstore: %w", err)
	}
	cloned, err := cloneFact(fact)
	if err != nil {
		return err
	}
	if cloned.RecordedAt.IsZero() {
		cloned.RecordedAt = s.now().UTC()
	}
	key := cloned.SourceEventKey()
	legacyKey := cloned.IdempotencyKey()

	s.mu.Lock()
	defer s.mu.Unlock()

	existingFacts := make([]metering.Fact, len(s.facts))
	for i, row := range s.facts {
		existingFacts[i] = row.fact
	}
	lookup := func(factID string) (metering.Fact, bool) {
		for _, f := range existingFacts {
			if strings.TrimSpace(f.FactID) == factID {
				return f, true
			}
		}
		return metering.Fact{}, false
	}
	if err := validateSupersessionGraph(cloned, lookup, supersessionEdgesFromFacts(existingFacts)); err != nil {
		return err
	}

	if idx, ok := s.bySource[key]; ok {
		existing := s.facts[idx].fact
		if metering.SameFactReplay(existing, cloned) {
			return nil
		}
		return fmt.Errorf("%w: stream_id=%q fact_id=%q stored_seq=%d new_seq=%d",
			ErrIdentityCollision, cloned.StreamID, cloned.FactID, existing.Sequence, cloned.Sequence)
	}
	// Legacy compatibility: rows indexed by IdempotencyKey before SourceEventKey.
	if legacyKey != key {
		if idx, ok := s.bySource[legacyKey]; ok {
			existing := s.facts[idx].fact
			if metering.SameFactReplay(existing, cloned) {
				return nil
			}
			return fmt.Errorf("%w: stream_id=%q fact_id=%q stored_seq=%d new_seq=%d",
				ErrIdentityCollision, cloned.StreamID, cloned.FactID, existing.Sequence, cloned.Sequence)
		}
	}

	s.seq++
	s.bySource[key] = len(s.facts)
	s.facts = append(s.facts, storedFact{seq: s.seq, source: key, fact: cloned})
	return nil
}

// List returns a bounded page of facts matching the query.
func (s *MemoryStore) List(ctx context.Context, q metering.Query) (metering.Page, error) {
	if err := ctx.Err(); err != nil {
		return metering.Page{}, err
	}
	unsupported := metering.QueryUnsupported(q)
	if err := metering.ValidateQuery(q); err != nil {
		return metering.Page{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = s.defaultPageSize
	}
	if limit > s.maxPageSize {
		limit = s.maxPageSize
	}
	offset := 0
	if cur := strings.TrimSpace(q.Cursor); cur != "" {
		n, err := strconv.Atoi(cur)
		if err != nil || n < 0 {
			return metering.Page{}, fmt.Errorf("metering/journalstore: invalid cursor")
		}
		offset = n
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	matched := make([]metering.Fact, 0)
	for _, row := range s.facts {
		if !metering.FactMatchesQuery(row.fact, q) {
			continue
		}
		cloned, err := cloneFact(row.fact)
		if err != nil {
			return metering.Page{}, err
		}
		matched = append(matched, cloned)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].StreamID != matched[j].StreamID {
			return matched[i].StreamID < matched[j].StreamID
		}
		return matched[i].Sequence < matched[j].Sequence
	})
	if offset > len(matched) {
		offset = len(matched)
	}
	end := min(offset+limit, len(matched))
	page := metering.Page{
		Facts:       matched[offset:end],
		Unsupported: append([]metering.UnsupportedFilter(nil), unsupported...),
	}
	if end < len(matched) {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func cloneFact(f metering.Fact) (metering.Fact, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return metering.Fact{}, fmt.Errorf("metering/journalstore: marshal fact: %w", err)
	}
	var out metering.Fact
	if err := json.Unmarshal(raw, &out); err != nil {
		return metering.Fact{}, fmt.Errorf("metering/journalstore: unmarshal fact: %w", err)
	}
	out.Scope = f.Scope.Clone()
	return out, nil
}

// filterPairs returns safe indexed projections for durable filter rows.
func filterPairs(f metering.Fact) [][2]string {
	pairs := [][2]string{
		{"stream_id", strings.TrimSpace(f.StreamID)},
		{"request_id", strings.TrimSpace(f.Correlation.RequestID)},
		{"a_leg_id", strings.TrimSpace(f.Correlation.ALegID)},
		{"b_leg_id", strings.TrimSpace(f.Correlation.BLegID)},
		{"attempt_id", strings.TrimSpace(f.Correlation.AttemptID)},
		{"perspective", string(f.Perspective)},
		{"boundary", string(f.Boundary)},
		{"lifecycle", string(f.Lifecycle)},
		{"kind", string(f.Kind)},
		{"presence", string(f.Presence)},
		{"frontend_id", strings.TrimSpace(f.FrontendID)},
		{"backend_id", strings.TrimSpace(f.BackendID)},
		{"model", strings.TrimSpace(f.Model)},
	}
	addScope := func(name string, v scope.Value) {
		if v.IsKnown() && strings.TrimSpace(v.Value) != "" {
			pairs = append(pairs, [2]string{name, strings.TrimSpace(v.Value)})
		}
	}
	addScope("principal_id", f.Scope.PrincipalID)
	addScope("credential_id", f.Scope.CredentialID)
	addScope("tenant_id", f.Scope.TenantID)
	addScope("organization_id", f.Scope.OrganizationID)
	addScope("workspace_id", f.Scope.WorkspaceID)
	addScope("project_id", f.Scope.ProjectID)
	addScope("department_id", f.Scope.DepartmentID)
	addScope("cost_center_id", f.Scope.CostCenterID)
	if id := strings.TrimSpace(f.PolicyVersion.ID); id != "" {
		pairs = append(pairs, [2]string{"rule_id", id})
	}
	out := make([][2]string, 0, len(pairs))
	for _, p := range pairs {
		if p[1] == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

var (
	_ metering.Recorder = (*MemoryStore)(nil)
	_ metering.Querier  = (*MemoryStore)(nil)
)
