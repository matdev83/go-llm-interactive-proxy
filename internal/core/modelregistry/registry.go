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
	ErrNilContext             = errors.New("modelregistry: nil context")
	ErrMissingProvider        = errors.New("modelregistry: missing inventory provider")
	ErrMissingBackendPrefix   = errors.New("modelregistry: missing backend prefix")
	ErrDuplicateBackendPrefix = errors.New("modelregistry: duplicate backend prefix")
	ErrInvalidModel           = errors.New("modelregistry: invalid model")
	ErrInvalidCanonicalID     = errors.New("modelregistry: invalid canonical model id")
)

type BackendInventory struct {
	BackendID       string
	Kind            string
	BackendPrefixes []string
	Provider        modelinventory.Provider
	FetchTimeout    time.Duration
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
	registeredPrefixes, err := validateInventoryPrefixes(inventories)
	if err != nil {
		return nil, err
	}
	byCanonical := make(map[string][]BackendModel)
	all := []BackendModel{}
	for i, inv := range inventories {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		backendID := strings.TrimSpace(inv.BackendID)
		kind := strings.TrimSpace(inv.Kind)
		if backendID == "" || kind == "" {
			return nil, fmt.Errorf("%w at inventory[%d]", ErrInvalidModel, i)
		}
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
			if canonicalUsesRegisteredPrefixQualifier(canonical, registeredPrefixes) {
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
	return newRegistryFromBackendModels(all, registeredPrefixes)
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

type prefixOwner struct {
	backendID string
	kind      string
}

func validateInventoryPrefixes(inventories []BackendInventory) (map[string]struct{}, error) {
	owners := make(map[string]prefixOwner)
	registered := make(map[string]struct{})
	for i, inv := range inventories {
		backendID := strings.TrimSpace(inv.BackendID)
		kind := strings.TrimSpace(inv.Kind)
		valid := normalizeBackendPrefixes(inv.BackendPrefixes)
		if len(valid) == 0 {
			if backendID == "" {
				backendID = fmt.Sprintf("inventory[%d]", i)
			}
			return nil, fmt.Errorf("%w for backend %q at inventory[%d]", ErrMissingBackendPrefix, backendID, i)
		}
		for _, prefix := range valid {
			if prev, ok := owners[prefix]; ok {
				if prev.kind != kind {
					return nil, fmt.Errorf("%w %q claimed by backend %q (kind %q) and backend %q (kind %q)", ErrDuplicateBackendPrefix, prefix, prev.backendID, prev.kind, backendID, kind)
				}
				continue
			}
			owners[prefix] = prefixOwner{backendID: backendID, kind: kind}
			registered[prefix] = struct{}{}
		}
	}
	return registered, nil
}

func normalizeBackendPrefixes(prefixes []string) []string {
	out := []string{}
	seen := make(map[string]struct{}, len(prefixes))
	for _, raw := range prefixes {
		prefix := strings.TrimSpace(raw)
		if prefix == "" || strings.Contains(prefix, "/") || strings.Contains(prefix, ":") {
			continue
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		out = append(out, prefix)
	}
	return out
}

func canonicalUsesRegisteredPrefixQualifier(canonical string, registeredPrefixes map[string]struct{}) bool {
	prefix, rest, ok := strings.Cut(canonical, ":")
	if !ok {
		return false
	}
	prefix = strings.TrimSpace(prefix)
	rest = strings.TrimSpace(rest)
	if prefix == "" || rest == "" {
		return false
	}
	_, registered := registeredPrefixes[prefix]
	return registered
}

func newRegistryFromBackendModels(models []BackendModel, registeredPrefixes map[string]struct{}) (*Registry, error) {
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
		if canonicalUsesRegisteredPrefixQualifier(canonical, registeredPrefixes) {
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
