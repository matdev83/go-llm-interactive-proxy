package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
)

type Store struct {
	path string
}

func New(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Load(ctx context.Context) (modelregistry.Snapshot, error) {
	if ctx == nil {
		return modelregistry.Snapshot{}, modelregistry.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return modelregistry.Snapshot{}, err
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return modelregistry.Snapshot{}, modelregistry.ErrSnapshotUnavailable
		}
		return modelregistry.Snapshot{}, fmt.Errorf("modelregistry cache load: %w", err)
	}
	var snap modelregistry.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return modelregistry.Snapshot{}, fmt.Errorf("modelregistry cache decode: %w", err)
	}
	if snap.Models == nil {
		snap.Models = []modelregistry.BackendModel{}
	}
	return snap, nil
}

func (s *Store) Save(ctx context.Context, snap modelregistry.Snapshot) error {
	if ctx == nil {
		return modelregistry.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("modelregistry cache mkdir: %w", err)
	}
	if snap.Models == nil {
		snap.Models = []modelregistry.BackendModel{}
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("modelregistry cache encode: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("modelregistry cache temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("modelregistry cache write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("modelregistry cache close: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("modelregistry cache replace: %w", err)
	}
	return nil
}
