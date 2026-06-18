// Package modelinventory defines the backend-owned model inventory contract.
package modelinventory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

var ErrNilContext = errors.New("modelinventory: nil context")

type Source string

const (
	SourceUnknown       Source = ""
	SourceRemote        Source = "remote"
	SourceStaticFile    Source = "static_file"
	SourceStaticInline  Source = "static_inline"
	SourceStaticBuiltin Source = "static_builtin"
)

type Model struct {
	CanonicalID string
	NativeID    string
	DisplayName string
}

type Snapshot struct {
	Source   Source
	LoadedAt time.Time
	Models   []Model
	Warnings []string
}

// Provider loads model inventory for one configured backend instance.
type Provider interface {
	LoadModels(ctx context.Context) (Snapshot, error)
}

// StaticInventory marks providers whose inventory is local and does not require periodic remote refresh.
type StaticInventory interface {
	StaticInventory() bool
}

type StaticProvider struct {
	Source   Source
	LoadedAt time.Time
	Models   []Model
	Warnings []string
}

func (p StaticProvider) StaticInventory() bool {
	return true
}

func (p StaticProvider) LoadModels(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	source := p.Source
	if source == "" {
		source = SourceStaticBuiltin
	}
	loadedAt := p.LoadedAt
	if loadedAt.IsZero() {
		loadedAt = time.Now()
	}
	return Snapshot{
		Source:   source,
		LoadedAt: loadedAt,
		Models:   slices.Clone(p.Models),
		Warnings: slices.Clone(p.Warnings),
	}, nil
}

type ErrorProvider struct {
	Err error
}

// StaticInventory marks ErrorProvider as non-refreshable so a cached snapshot
// loaded at startup is not repeatedly overwritten by the same construction error.
func (p ErrorProvider) StaticInventory() bool { return true }

func (p ErrorProvider) LoadModels(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, ErrNilContext
	}
	if p.Err == nil {
		return Snapshot{}, fmt.Errorf("modelinventory: unavailable")
	}
	return Snapshot{}, p.Err
}
