package modelregistry

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

var ErrSnapshotUnavailable = errors.New("modelregistry: snapshot unavailable")

type RefreshFailureCategory string

const (
	RefreshFailureNone  RefreshFailureCategory = ""
	RefreshFailureFetch RefreshFailureCategory = "fetch"
	RefreshFailureCache RefreshFailureCategory = "cache"
	RefreshFailureParse RefreshFailureCategory = "parse"
)

type Cache interface {
	Load(ctx context.Context) (Snapshot, error)
	Save(ctx context.Context, snap Snapshot) error
}

type RuntimeConfig struct {
	Inventories []BackendInventory
	Cache       Cache
	Now         func() time.Time
	// Log receives fail-soft inventory warnings from Build. Nil disables logging.
	Log *slog.Logger
}

// published is the atomic unit of registry visibility: Diagnostics (snap) and
// Lookup/All (reg) always observe the same generation. modelsJSON is the
// immutable OpenAI /v1/models body for this generation; fingerprint skips
// allowlist rebuilds when catalog content is unchanged.
type published struct {
	reg                *Registry
	snap               Snapshot
	modelsJSON         []byte
	fingerprint        uint64
	backendModelCounts map[string]int
}

type Runtime struct {
	cfg RuntimeConfig

	published  atomic.Pointer[published]
	refreshing atomic.Bool

	mu          sync.Mutex
	lastFail    RefreshFailureCategory
	cacheFail   RefreshFailureCategory
	discoveries []BackendDiscovery
}

type Diagnostics struct {
	Active                   bool
	Generation               string
	RefreshedAt              time.Time
	ModelCount               int
	BackendModelCounts       map[string]int
	BackendDiscoveries       []BackendDiscovery
	LastRefreshErrorCategory RefreshFailureCategory
	LastCacheErrorCategory   RefreshFailureCategory
}

func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Inventories = slices.Clone(cfg.Inventories)
	return &Runtime{cfg: cfg}
}

func (r *Runtime) Start(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}
	if r.cfg.Cache != nil {
		snap, err := r.cfg.Cache.Load(ctx)
		if err == nil {
			if err := r.publishSnapshot(snap, discoveriesFromCache(r.cfg.Inventories, snap)); err == nil {
				r.setOK()
				r.setCacheOK()
				return nil
			}
			r.setCacheFailure(RefreshFailureParse)
		} else if !errors.Is(err, ErrSnapshotUnavailable) {
			r.setCacheFailure(RefreshFailureCache)
		}
	}
	if err := r.refresh(ctx); err != nil {
		return err
	}
	return nil
}

// RunRefresh runs a background inventory refresh. Concurrent calls while a
// refresh is already in flight are skipped (no stampede of LoadModels/CLI spawns).
func (r *Runtime) RunRefresh(ctx context.Context) {
	if ctx == nil {
		r.setFailure(RefreshFailureFetch)
		return
	}
	if !r.refreshing.CompareAndSwap(false, true) {
		return
	}
	defer r.refreshing.Store(false)

	if err := r.refresh(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		var refreshErr refreshFailureError
		if errors.As(err, &refreshErr) {
			r.setFailure(refreshErr.category)
			return
		}
		r.setFailure(RefreshFailureFetch)
	}
}

func (r *Runtime) Lookup(canonicalID string) ([]BackendModel, bool) {
	reg := r.ActiveRegistry()
	if reg == nil {
		return nil, false
	}
	return reg.Lookup(canonicalID)
}

func (r *Runtime) All() []BackendModel {
	reg := r.ActiveRegistry()
	if reg == nil {
		return []BackendModel{}
	}
	return reg.All()
}

// ModelsJSON returns the precomputed OpenAI /v1/models JSON body and generation
// ETag for the published snapshot. ok is false when nothing is published.
func (r *Runtime) ModelsJSON() (body []byte, generation string, ok bool) {
	if r == nil {
		return nil, "", false
	}
	pub := r.published.Load()
	if pub == nil {
		return nil, "", false
	}
	return pub.modelsJSON, pub.snap.Generation, true
}

func (r *Runtime) Diagnostics() Diagnostics {
	if r == nil {
		return Diagnostics{
			BackendModelCounts: map[string]int{},
			BackendDiscoveries: []BackendDiscovery{},
		}
	}
	out := Diagnostics{
		BackendModelCounts: map[string]int{},
		BackendDiscoveries: []BackendDiscovery{},
	}
	pub := r.published.Load()
	r.mu.Lock()
	out.LastRefreshErrorCategory = r.lastFail
	out.LastCacheErrorCategory = r.cacheFail
	out.BackendDiscoveries = slices.Clone(r.discoveries)
	r.mu.Unlock()
	if out.BackendDiscoveries == nil {
		out.BackendDiscoveries = []BackendDiscovery{}
	}
	if pub == nil {
		return out
	}
	out.Active = true
	out.Generation = pub.snap.Generation
	out.RefreshedAt = pub.snap.RefreshedAt
	out.ModelCount = len(pub.snap.Models)
	out.BackendModelCounts = cloneIntMap(pub.backendModelCounts)
	return out
}

func (r *Runtime) Discoveries() []BackendDiscovery {
	if r == nil {
		return []BackendDiscovery{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.discoveries)
	if out == nil {
		return []BackendDiscovery{}
	}
	return out
}

func (r *Runtime) ActiveRegistry() *Registry {
	if r == nil {
		return nil
	}
	pub := r.published.Load()
	if pub == nil {
		return nil
	}
	return pub.reg
}

func (r *Runtime) LastRefreshFailure() RefreshFailureCategory {
	if r == nil {
		return RefreshFailureNone
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastFail
}

func (r *Runtime) LastCacheFailure() RefreshFailureCategory {
	if r == nil {
		return RefreshFailureNone
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cacheFail
}

func (r *Runtime) refresh(ctx context.Context) error {
	built, err := Build(ctx, r.cfg.Inventories, r.cfg.Log)
	if err != nil {
		return refreshFailureError{category: RefreshFailureFetch, err: err}
	}

	reg := built.Registry
	if reg == nil || reg.Len() == 0 {
		if prev := r.published.Load(); prev != nil && prev.reg != nil && prev.reg.Len() > 0 {
			// Retain last-good registry. Publish discovery status for the failed
			// attempt, then re-sync allowlists to the retained snapshot.
			r.setDiscoveries(built.Discoveries)
			r.syncAllowlists(prev.snap.Models)
			return refreshFailureError{category: RefreshFailureFetch, err: ErrNoUsableInventory}
		}
		reg = &Registry{byCanonical: map[string][]BackendModel{}, all: []BackendModel{}}
	}

	// One clone for cache Save + publish ownership; Registry.all stays immutable.
	modelsCopy := slices.Clone(reg.all)
	if modelsCopy == nil {
		modelsCopy = []BackendModel{}
	}
	snap := Snapshot{
		Generation:  strconv.FormatInt(r.cfg.Now().UnixNano(), 10),
		RefreshedAt: r.cfg.Now().UTC(),
		Models:      modelsCopy,
	}
	if r.cfg.Cache != nil {
		if err := r.cfg.Cache.Save(ctx, snap); err != nil {
			r.publish(reg, snap, built.Discoveries)
			r.setOK()
			r.setCacheFailure(RefreshFailureCache)
			return nil
		}
	}
	r.publish(reg, snap, built.Discoveries)
	r.setOK()
	r.setCacheOK()
	return nil
}

func (r *Runtime) publishSnapshot(snap Snapshot, discoveries []BackendDiscovery) error {
	if err := r.validateSnapshotBackends(snap); err != nil {
		return err
	}
	registeredPrefixes, err := validateInventoryPrefixes(r.cfg.Inventories)
	if err != nil {
		return err
	}
	reg, err := newRegistryFromBackendModels(snap.Models, registeredPrefixes)
	if err != nil {
		return err
	}
	r.publish(reg, snap, discoveries)
	return nil
}

func (r *Runtime) validateSnapshotBackends(snap Snapshot) error {
	allowed := make(map[string]struct{}, len(r.cfg.Inventories))
	for _, inv := range r.cfg.Inventories {
		if id := strings.TrimSpace(inv.BackendID); id != "" {
			allowed[id] = struct{}{}
		}
	}
	for i, model := range snap.Models {
		backendID := strings.TrimSpace(model.BackendID)
		if _, ok := allowed[backendID]; !ok {
			return fmt.Errorf("%w: cached model[%d] references unconfigured backend %q", ErrInvalidModel, i, backendID)
		}
	}
	return nil
}

// publish flips registry+snapshot atomically, then records discoveries, then
// commits connector allowlists when catalog content changed. Order is intentional:
//  1. published Store — /v1/models and Lookup see one generation together
//  2. discoveries — diagnostics match that generation (not a prior Build)
//  3. syncAllowlists — allowlists never advance before the registry (Open must
//     not reject models still advertised). Brief lag after Store where new
//     models are listed before Open allowlists catch up is fail-closed.
func (r *Runtime) publish(reg *Registry, snap Snapshot, discoveries []BackendDiscovery) {
	fp := fingerprintModels(snap.Models)
	prev := r.published.Load()
	contentChanged := prev == nil || prev.fingerprint != fp
	generation := snap.Generation
	if !contentChanged && prev != nil {
		generation = prev.snap.Generation
	}
	// Take ownership of snap.Models when refresh already cloned; still clone for
	// cache-hydration paths that may share the cache's backing slice.
	models := snap.Models
	if models == nil {
		models = []BackendModel{}
	} else {
		models = slices.Clone(snap.Models)
	}
	cp := Snapshot{
		Generation:  generation,
		RefreshedAt: snap.RefreshedAt,
		Models:      models,
	}
	modelsJSON, err := MarshalOpenAIModelsListJSON(models)
	if err != nil {
		// Defensive: BuildOpenAIModelsList only uses strings; marshal should not fail.
		modelsJSON = []byte(`{"object":"list","data":[]}`)
	}
	counts := make(map[string]int, len(r.cfg.Inventories))
	for _, row := range models {
		counts[row.BackendID]++
	}
	r.published.Store(&published{
		reg:                reg,
		snap:               cp,
		modelsJSON:         modelsJSON,
		fingerprint:        fp,
		backendModelCounts: counts,
	})
	r.setDiscoveries(discoveries)
	if contentChanged {
		r.syncAllowlists(cp.Models)
	}
}

// syncAllowlists aligns provider-local allowlists with the published registry.
// Backends present in models receive those rows; configured backends absent from
// the snapshot receive an empty AcceptInventory (clear). Providers that do not
// implement AcceptedInventory are skipped.
func (r *Runtime) syncAllowlists(models []BackendModel) {
	if r == nil {
		return
	}
	byBackend := make(map[string][]modelinventory.Model, len(r.cfg.Inventories))
	for _, m := range models {
		id := strings.TrimSpace(m.BackendID)
		byBackend[id] = append(byBackend[id], modelinventory.Model{
			CanonicalID: m.CanonicalID,
			NativeID:    m.NativeID,
			DisplayName: m.DisplayName,
		})
	}
	for _, inv := range r.cfg.Inventories {
		a, ok := inv.Provider.(modelinventory.AcceptedInventory)
		if !ok {
			continue
		}
		id := strings.TrimSpace(inv.BackendID)
		a.AcceptInventory(byBackend[id]) // nil clears allowlist (len==0)
	}
}

func (r *Runtime) setFailure(cat RefreshFailureCategory) {
	r.mu.Lock()
	r.lastFail = cat
	r.mu.Unlock()
}

func (r *Runtime) setOK() {
	r.setFailure(RefreshFailureNone)
}

func (r *Runtime) setCacheFailure(cat RefreshFailureCategory) {
	r.mu.Lock()
	r.cacheFail = cat
	r.mu.Unlock()
}

func (r *Runtime) setCacheOK() {
	r.setCacheFailure(RefreshFailureNone)
}

func (r *Runtime) setDiscoveries(discoveries []BackendDiscovery) {
	r.mu.Lock()
	r.discoveries = slices.Clone(discoveries)
	r.mu.Unlock()
}

func discoveriesFromCache(inventories []BackendInventory, snap Snapshot) []BackendDiscovery {
	counts := make(map[string]int, len(inventories))
	sources := make(map[string]modelinventory.Source, len(inventories))
	for _, row := range snap.Models {
		id := strings.TrimSpace(row.BackendID)
		counts[id]++
		if _, ok := sources[id]; !ok {
			sources[id] = row.Source
		}
	}
	out := make([]BackendDiscovery, 0, len(inventories))
	for _, inv := range inventories {
		id := strings.TrimSpace(inv.BackendID)
		kind := strings.TrimSpace(inv.Kind)
		count := counts[id]
		status := modelinventory.DiscoveryStatusCached
		errorCode := modelinventory.ErrorCodeNone
		if count == 0 {
			// Configured backend present in inventories but absent from the
			// cached snapshot: report empty, not cached-with-zero-rows.
			status = modelinventory.DiscoveryStatusEmpty
			errorCode = modelinventory.ErrorCodeEmpty
		}
		out = append(out, newBackendDiscovery(id, kind, modelinventory.Discovery{
			Status:     status,
			Source:     sources[id],
			ModelCount: count,
			ErrorCode:  errorCode,
		}))
	}
	return out
}

func fingerprintModels(models []BackendModel) uint64 {
	type row struct {
		backendID, canonical, native, kind string
	}
	rows := make([]row, 0, len(models))
	for _, m := range models {
		rows = append(rows, row{
			backendID: strings.TrimSpace(m.BackendID),
			canonical: strings.TrimSpace(m.CanonicalID),
			native:    strings.TrimSpace(m.NativeID),
			kind:      strings.TrimSpace(m.Kind),
		})
	}
	slices.SortFunc(rows, func(a, b row) int {
		if c := strings.Compare(a.backendID, b.backendID); c != 0 {
			return c
		}
		if c := strings.Compare(a.canonical, b.canonical); c != 0 {
			return c
		}
		if c := strings.Compare(a.native, b.native); c != 0 {
			return c
		}
		return strings.Compare(a.kind, b.kind)
	})
	h := fnv.New64a()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(len(rows)))
	_, _ = h.Write(buf[:])
	for _, r := range rows {
		_, _ = h.Write([]byte(r.backendID))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(r.canonical))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(r.native))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(r.kind))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

func cloneIntMap(in map[string]int) map[string]int {
	if len(in) == 0 {
		return map[string]int{}
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type refreshFailureError struct {
	category RefreshFailureCategory
	err      error
}

func (e refreshFailureError) Error() string {
	if e.err == nil {
		return "modelregistry: refresh failure"
	}
	return e.err.Error()
}

func (e refreshFailureError) Unwrap() error {
	return e.err
}
