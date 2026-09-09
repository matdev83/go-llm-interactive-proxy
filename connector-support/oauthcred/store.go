package oauthcred

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Store defines the persistent storage interface for TokenRecord.
type Store interface {
	Load() (TokenRecord, error)
	Save(rec TokenRecord) error
	Delete() error
	Path() string
}

// FileStore is a file-backed TokenRecord store with atomic writes and restrictive permissions.
type FileStore struct {
	path string
	mu   sync.Mutex
}

// NewFileStore creates a new FileStore at the specified path.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: strings.TrimSpace(path)}
}

// Path returns the storage file path.
func (s *FileStore) Path() string {
	return s.path
}

// Load reads and unmarshals the TokenRecord from disk after validating file permissions.
func (s *FileStore) Load() (TokenRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.path == "" {
		return TokenRecord{}, fmt.Errorf("oauthcred: store path is empty")
	}

	if err := checkTokenFilePermissions(s.path); err != nil {
		return TokenRecord{}, err
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		return TokenRecord{}, err
	}

	var rec TokenRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return TokenRecord{}, fmt.Errorf("oauthcred: invalid token JSON: %w", err)
	}

	return rec, nil
}

// Save atomically writes the TokenRecord to disk with 0600 permissions.
func (s *FileStore) Save(rec TokenRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.path == "" {
		return fmt.Errorf("oauthcred: store path is empty")
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("oauthcred: create directory: %w", err)
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("oauthcred: marshal token record: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, "token-*.tmp")
	if err != nil {
		return fmt.Errorf("oauthcred: create temp token file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("oauthcred: chmod temp token file: %w", err)
	}

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("oauthcred: write temp token file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("oauthcred: close temp token file: %w", err)
	}

	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("oauthcred: rename temp token file: %w", err)
	}

	return nil
}

// Delete removes the token record file.
func (s *FileStore) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.path == "" {
		return nil
	}

	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("oauthcred: remove token file: %w", err)
	}
	return nil
}

// checkTokenFilePermissions rejects token files readable or writable by group
// or other on Unix, mirroring the Codex CLI auth.json guard. On Windows
// (ACL-based permissions, no meaningful Unix mode bits) it is a no-op.
func checkTokenFilePermissions(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil // let caller's ReadFile produce canonical not-exist error
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("oauthcred: token file %q is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("oauthcred: token file %q is group/other accessible (mode %o); expected 0600", path, info.Mode().Perm())
	}
	return nil
}
