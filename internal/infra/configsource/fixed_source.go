package configsource

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FixedSource is the startup-fixed absolute configuration path used by every
// reload. It does not watch the filesystem; callers must invoke ReadStable
// explicitly after an operator trigger.
type FixedSource struct {
	path     string
	maxBytes int64
}

// NewFixedSource resolves path to an absolute location and stores the
// startup-fixed size limit. maxBytes <= 0 selects DefaultMaxBytes.
func NewFixedSource(path string, maxBytes int64) (*FixedSource, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("configsource: empty path")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("configsource: resolve path: %w", err)
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &FixedSource{path: abs, maxBytes: maxBytes}, nil
}

// AbsolutePath returns the startup-resolved absolute source path.
func (s *FixedSource) AbsolutePath() string {
	if s == nil {
		return ""
	}
	return s.path
}

// MaxBytes returns the startup-fixed read limit.
func (s *FixedSource) MaxBytes() int64 {
	if s == nil || s.maxBytes <= 0 {
		return DefaultMaxBytes
	}
	return s.maxBytes
}

// ReadStable opens the fixed path, captures handle identity, reads one bounded
// snapshot, revalidates the same handle and path identity, classifies the
// bytes, and enforces atomic-replacement rules against active when non-nil.
//
// On success AtomicResult is AtomicEligible (first read / new identity),
// AtomicNoop (same identity+digest), or the call fails with
// CategoryNonAtomicUpdate when the same identity changed digest.
func (s *FixedSource) ReadStable(ctx context.Context, active *ActiveSourceVersion) (SourceSnapshot, AtomicResult, error) {
	var zero SourceSnapshot
	if s == nil || s.path == "" {
		return zero, "", fmt.Errorf("configsource: nil source")
	}
	if err := ctx.Err(); err != nil {
		return zero, "", err
	}

	f, err := os.Open(s.path) // #nosec G304 -- fixed absolute startup path
	if err != nil {
		if os.IsNotExist(err) {
			return zero, "", integrityErr(CategoryMissing)
		}
		return zero, "", fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	defer func() { _ = f.Close() }()

	beforeInfo, err := f.Stat()
	if err != nil {
		return zero, "", fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	if !beforeInfo.Mode().IsRegular() {
		return zero, "", integrityErr(CategoryUnsupportedType)
	}
	beforeID, err := identityFromFileInfo(beforeInfo)
	if err != nil {
		return zero, "", err
	}
	beforeSize := beforeInfo.Size()

	limited := io.LimitReader(f, s.maxBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return zero, "", fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	if err := ctx.Err(); err != nil {
		return zero, "", err
	}

	afterInfo, err := f.Stat()
	if err != nil {
		return zero, "", fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	afterID, err := identityFromFileInfo(afterInfo)
	if err != nil {
		return zero, "", err
	}
	if cat, sterr := ClassifyStability(beforeSize, afterInfo.Size(), beforeID, afterID); sterr != nil {
		return zero, "", &IntegrityError{Category: cat}
	}

	pathInfo, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return zero, "", integrityErr(CategoryUnstable)
		}
		return zero, "", fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	if !pathInfo.Mode().IsRegular() {
		return zero, "", integrityErr(CategoryUnstable)
	}
	pathID, err := identityFromFileInfo(pathInfo)
	if err != nil {
		return zero, "", err
	}
	if pathID != beforeID || pathInfo.Size() != beforeSize {
		return zero, "", integrityErr(CategoryUnstable)
	}

	if int64(len(raw)) > s.maxBytes {
		return zero, "", oversizeErr(s.maxBytes)
	}
	if cat, cerr := ClassifyBytes(raw, s.maxBytes); cerr != nil {
		return zero, "", &IntegrityError{Category: cat}
	}

	digest := sha256.Sum256(raw)
	snap := SourceSnapshot{
		SourceID:       s.path,
		HandleIdentity: beforeID,
		Size:           int64(len(raw)),
		ModTime:        beforeInfo.ModTime().UTC(),
		PrivateDigest:  digest,
		Bytes:          raw,
		ReadAt:         time.Now().UTC(),
	}

	if active == nil {
		return snap, AtomicEligible, nil
	}
	res, cat, aerr := ClassifyAtomicReplacement(*active, snap)
	if aerr != nil {
		return zero, res, &IntegrityError{Category: cat}
	}
	return snap, res, nil
}
