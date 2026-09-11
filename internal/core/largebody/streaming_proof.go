package largebody

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonshape"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ReplayChunkReaderConfig configures reading replay bytes in fixed-size buffers.
type ReplayChunkReaderConfig struct {
	Reader    io.Reader
	MaxBytes  int64
	ChunkSize int
	Scanner   *jsonshape.Scanner
	Hasher    hash.Hash
	OnChunk   func(chunk []byte) error
}

// ReplayChunkReaderResult summarizes the outcome of reading replay chunks.
type ReplayChunkReaderResult struct {
	BytesRead int64
	BodyHash  [32]byte
}

// ProcessReplayChunks reads replay bytes from Reader in fixed-size buffers (default 32 KiB),
// simultaneously streaming them into Hasher (if non-nil), Scanner (if non-nil),
// and OnChunk (if non-nil) in one pass without calling io.ReadAll.
// It enforces MaxBytes if > 0.
func ProcessReplayChunks(cfg ReplayChunkReaderConfig) (ReplayChunkReaderResult, error) {
	if cfg.Reader == nil {
		return ReplayChunkReaderResult{}, errors.New("largebody: nil replay reader")
	}
	chunkSize := cfg.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 32 * 1024
	}
	buf := make([]byte, chunkSize)
	var totalRead int64

	for {
		n, readErr := cfg.Reader.Read(buf)
		if n > 0 {
			totalRead += int64(n)
			if cfg.MaxBytes > 0 && totalRead > cfg.MaxBytes {
				return ReplayChunkReaderResult{BytesRead: totalRead},
					fmt.Errorf("largebody: replay exceeded max bytes (%d > %d)", totalRead, cfg.MaxBytes)
			}
			chunk := buf[:n]
			if cfg.Hasher != nil {
				cfg.Hasher.Write(chunk)
			}
			if cfg.Scanner != nil {
				if err := cfg.Scanner.Feed(chunk); err != nil {
					return ReplayChunkReaderResult{BytesRead: totalRead},
						fmt.Errorf("largebody: scanner feed: %w", err)
				}
			}
			if cfg.OnChunk != nil {
				if err := cfg.OnChunk(chunk); err != nil {
					return ReplayChunkReaderResult{BytesRead: totalRead}, err
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return ReplayChunkReaderResult{BytesRead: totalRead},
				fmt.Errorf("largebody: read replay chunk: %w", readErr)
		}
	}

	if cfg.Scanner != nil {
		if _, err := cfg.Scanner.Finish(); err != nil {
			return ReplayChunkReaderResult{BytesRead: totalRead},
				fmt.Errorf("largebody: scanner finish: %w", err)
		}
	}

	var bodyHash [32]byte
	if cfg.Hasher != nil {
		cfg.Hasher.Sum(bodyHash[:0])
	}

	return ReplayChunkReaderResult{
		BytesRead: totalRead,
		BodyHash:  bodyHash,
	}, nil
}

// StreamingProofConfig configures streaming proof compilation across replay chunks.
type StreamingProofConfig struct {
	Reader             io.Reader
	MaxBytes           int64
	ChunkSize          int
	CallIdentityConfig CallIdentityConfig
	ScannerLimits      jsonshape.Limits
	ScannerOptions     []jsonshape.Option
	OnChunk            func(chunk []byte) error
}

// StreamingProofResult returns the compiled proof output from streaming execution.
type StreamingProofResult struct {
	BytesRead int64
	BodyHash  [32]byte
	Digest    IdentityDigest
}

// CompileStreamingProof compiles proof and semantic identity from replay bytes in a single pass.
// It feeds replay bytes through jsonshape.Scanner, computes raw body SHA-256, and drives
// CallIdentityWriter without allocating or retaining the full body in memory.
func CompileStreamingProof(ctx context.Context, cfg StreamingProofConfig) (StreamingProofResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	idWriter, err := NewCallIdentityWriter(cfg.CallIdentityConfig)
	if err != nil {
		return StreamingProofResult{}, fmt.Errorf("largebody: new identity writer: %w", err)
	}

	hasher := sha256.New()

	scannerOpts := append([]jsonshape.Option{
		jsonshape.WithStringWriterResolver(func(sctx jsonshape.StringContext) (io.Writer, error) {
			if sctx.TopLevel && sctx.Key == "input" {
				mw, err := idWriter.BeginMessage(lipapi.RoleUser)
				if err != nil {
					return nil, err
				}
				return mw.BeginTextPart()
			}
			return nil, nil
		}),
	}, cfg.ScannerOptions...)

	limits := cfg.ScannerLimits
	if limits.MaxBytes <= 0 && cfg.MaxBytes > 0 {
		limits.MaxBytes = cfg.MaxBytes
	}

	scanner := jsonshape.NewScanner(ctx, limits, scannerOpts...)

	readRes, err := ProcessReplayChunks(ReplayChunkReaderConfig{
		Reader:    cfg.Reader,
		MaxBytes:  cfg.MaxBytes,
		ChunkSize: cfg.ChunkSize,
		Scanner:   scanner,
		Hasher:    hasher,
		OnChunk:   cfg.OnChunk,
	})
	if err != nil {
		return StreamingProofResult{}, err
	}

	digest, err := idWriter.Digest()
	if err != nil {
		return StreamingProofResult{}, fmt.Errorf("largebody: digest: %w", err)
	}

	return StreamingProofResult{
		BytesRead: readRes.BytesRead,
		BodyHash:  readRes.BodyHash,
		Digest:    digest,
	}, nil
}
