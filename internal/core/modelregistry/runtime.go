package modelregistry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"
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
}

type Runtime struct {
	cfg RuntimeConfig

	active atomic.Pointer[Registry]
	snap   atomic.Pointer[Snapshot]

	mu        sync.Mutex
	lastFail  RefreshFailureCategory
	cacheFail RefreshFailureCategory
}

type Diagnostics struct {
	Active                   bool
	Generation               string
	RefreshedAt              time.Time
	ModelCount               int
	BackendModelCounts       map[string]int
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
			if err := r.publishSnapshot(snap); err == nil {
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

func (r *Runtime) RunRefresh(ctx context.Context) {
	if ctx == nil {
		r.setFailure(RefreshFailureFetch)
		return
	}
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

func (r *Runtime) Diagnostics() Diagnostics {
	if r == nil {
		return Diagnostics{
			BackendModelCounts: map[string]int{},
		}
	}
	out := Diagnostics{
		BackendModelCounts:       map[string]int{},
		LastRefreshErrorCategory: r.LastRefreshFailure(),
		LastCacheErrorCategory:   r.LastCacheFailure(),
	}
	snap := r.snap.Load()
	if snap == nil {
		return out
	}
	out.Active = true
	out.Generation = snap.Generation
	out.RefreshedAt = snap.RefreshedAt
	out.ModelCount = len(snap.Models)
	for _, row := range snap.Models {
		out.BackendModelCounts[row.BackendID]++
	}
	return out
}

func (r *Runtime) ActiveRegistry() *Registry {
	if r == nil {
		return nil
	}
	return r.active.Load()
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
	reg, err := Build(ctx, r.cfg.Inventories)
	if err != nil {
		return refreshFailureError{category: RefreshFailureFetch, err: err}
	}
	snap := Snapshot{
		Generation:  fmt.Sprintf("%d", r.cfg.Now().UnixNano()),
		RefreshedAt: r.cfg.Now().UTC(),
		Models:      reg.All(),
	}
	if r.cfg.Cache != nil {
		if err := r.cfg.Cache.Save(ctx, snap); err != nil {
			return refreshFailureError{category: RefreshFailureCache, err: err}
		}
	}
	r.publish(reg, snap)
	r.setOK()
	r.setCacheOK()
	return nil
}

func (r *Runtime) publishSnapshot(snap Snapshot) error {
	reg, err := newRegistryFromBackendModels(snap.Models)
	if err != nil {
		return err
	}
	r.publish(reg, snap)
	return nil
}

func (r *Runtime) publish(reg *Registry, snap Snapshot) {
	cp := Snapshot{
		Generation:  snap.Generation,
		RefreshedAt: snap.RefreshedAt,
		Models:      slices.Clone(snap.Models),
	}
	r.snap.Store(&cp)
	r.active.Store(reg)
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

type refreshFailureError struct {
	category RefreshFailureCategory
	err      error
}

func (e refreshFailureError) Error() string {
	return e.err.Error()
}

func (e refreshFailureError) Unwrap() error {
	return e.err
}
