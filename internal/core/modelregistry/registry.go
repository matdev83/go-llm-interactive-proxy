// Package modelregistry provides core-owned lookup over backend-exposed models.
package modelregistry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

var (
	ErrNilContext         = errors.New("modelregistry: nil context")
	ErrMissingProvider    = errors.New("modelregistry: missing inventory provider")
	ErrInvalidModel       = errors.New("modelregistry: invalid model")
	ErrInvalidCanonicalID = errors.New("modelregistry: invalid canonical model id")
)

type BackendInventory struct {
	BackendID    string
	Kind         string
	Provider     modelinventory.Provider
	FetchTimeout time.Duration
}

type BackendModel struct {
	CanonicalID string
	NativeID    string
	DisplayName string
	BackendID   string
	Kind        string
	Source      modelinventory.Source
	LoadedAt    time.Time
}

type Snapshot struct {
	Generation  string
	RefreshedAt time.Time
	Models      []BackendModel
}

type Registry struct {
	byCanonical map[string][]BackendModel
	all         []BackendModel
}

func Build(ctx context.Context, inventories []BackendInventory) (*Registry, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	byCanonical := make(map[string][]BackendModel)
	all := []BackendModel{}
	for i, inv := range inventories {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		backendID := strings.TrimSpace(inv.BackendID)
		kind := strings.TrimSpace(inv.Kind)
		if inv.Provider == nil {
			return nil, fmt.Errorf("%w for backend %q", ErrMissingProvider, backendID)
		}
		loadCtx := ctx
		var cancel context.CancelFunc
		if inv.FetchTimeout > 0 {
			loadCtx, cancel = context.WithTimeout(ctx, inv.FetchTimeout)
		}
		snap, err := inv.Provider.LoadModels(loadCtx)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			return nil, fmt.Errorf("backend %q model inventory: %w", backendID, err)
		}
		for j, m := range snap.Models {
			canonical := strings.TrimSpace(m.CanonicalID)
			native := strings.TrimSpace(m.NativeID)
			if canonical == "" || native == "" {
				return nil, fmt.Errorf("%w for backend %q model[%d]", ErrInvalidModel, backendID, j)
			}
			if !validCanonicalID(canonical) {
				return nil, fmt.Errorf("%w %q for backend %q model[%d]", ErrInvalidCanonicalID, canonical, backendID, j)
			}
			row := BackendModel{
				CanonicalID: canonical,
				NativeID:    native,
				DisplayName: strings.TrimSpace(m.DisplayName),
				BackendID:   backendID,
				Kind:        kind,
				Source:      snap.Source,
				LoadedAt:    snap.LoadedAt,
			}
			byCanonical[canonical] = append(byCanonical[canonical], row)
			all = append(all, row)
		}
		if len(snap.Models) == 0 {
			return nil, fmt.Errorf("%w: backend %q returned no models at inventory[%d]", ErrInvalidModel, backendID, i)
		}
	}
	if len(all) == 0 {
		return &Registry{
			byCanonical: map[string][]BackendModel{},
			all:         []BackendModel{},
		}, nil
	}
	return newRegistryFromBackendModels(all)
}

func (r *Registry) Lookup(canonicalID string) ([]BackendModel, bool) {
	if r == nil {
		return nil, false
	}
	models, ok := r.byCanonical[strings.TrimSpace(canonicalID)]
	if !ok || len(models) == 0 {
		return nil, false
	}
	return slices.Clone(models), true
}

func (r *Registry) All() []BackendModel {
	if r == nil || len(r.all) == 0 {
		return []BackendModel{}
	}
	return slices.Clone(r.all)
}

func validCanonicalID(id string) bool {
	left, right, ok := strings.Cut(id, "/")
	if !ok {
		return false
	}
	return strings.TrimSpace(left) != "" && strings.TrimSpace(right) != "" && !strings.Contains(right, "/")
}

func newRegistryFromBackendModels(models []BackendModel) (*Registry, error) {
	byCanonical := make(map[string][]BackendModel)
	all := make([]BackendModel, 0, len(models))
	for i, m := range models {
		canonical := strings.TrimSpace(m.CanonicalID)
		native := strings.TrimSpace(m.NativeID)
		backendID := strings.TrimSpace(m.BackendID)
		kind := strings.TrimSpace(m.Kind)
		if canonical == "" || native == "" || backendID == "" {
			return nil, fmt.Errorf("%w at model[%d]", ErrInvalidModel, i)
		}
		if !validCanonicalID(canonical) {
			return nil, fmt.Errorf("%w %q at model[%d]", ErrInvalidCanonicalID, canonical, i)
		}
		row := BackendModel{
			CanonicalID: canonical,
			NativeID:    native,
			DisplayName: strings.TrimSpace(m.DisplayName),
			BackendID:   backendID,
			Kind:        kind,
			Source:      m.Source,
			LoadedAt:    m.LoadedAt,
		}
		byCanonical[canonical] = append(byCanonical[canonical], row)
		all = append(all, row)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("%w: no models", ErrInvalidModel)
	}
	return &Registry{
		byCanonical: byCanonical,
		all:         all,
	}, nil
}
