package contract

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
)

// LargePayloadInput parameterizes the large-payload fast path for a frontend mount generation
// (Requirements 1, 2, 20).
type LargePayloadInput struct {
	// Enabled gates fast-path candidate evaluation. Default false.
	Enabled bool
	// ThresholdBytes is the decoded-size consideration gate. When <= 0,
	// largebody.DefaultThresholdBytes (1 MiB) is used.
	ThresholdBytes int64
	// WireEligibility optionally supplies the generation-frozen WireEligibilitySummary.
	WireEligibility largebody.WireEligibilitySummary
	// SpoolLedger optionally supplies the shared in-flight logical spool ledger.
	SpoolLedger *largebody.SpoolLedger
	// MemorySpoolBytes bounds retained bytes in Go heap per capture before spilling.
	MemorySpoolBytes int64
	// SpoolDir is the directory where temporary spill files are created.
	SpoolDir string
	// CopyBufferSize is the chunk size used for reading from the client body.
	CopyBufferSize int
	// Diagnostics optionally supplies a diagnostic observer.
	Diagnostics largebody.DiagnosticsObserver
}

// EffectiveThresholdBytes returns ThresholdBytes if > 0, else DefaultThresholdBytes (1 MiB).
func (c LargePayloadInput) EffectiveThresholdBytes() int64 {
	if c.ThresholdBytes > 0 {
		return c.ThresholdBytes
	}
	return largebody.DefaultThresholdBytes
}

// EffectiveMemorySpoolBytes returns MemorySpoolBytes if > 0, else DefaultMemorySpoolBytes (64 KiB).
func (c LargePayloadInput) EffectiveMemorySpoolBytes() int64 {
	if c.MemorySpoolBytes > 0 {
		return c.MemorySpoolBytes
	}
	return largebody.DefaultMemorySpoolBytes
}

// EffectiveCopyBufferSize returns CopyBufferSize if > 0, else DefaultCopyBufferSize (32 KiB).
func (c LargePayloadInput) EffectiveCopyBufferSize() int {
	if c.CopyBufferSize > 0 {
		return c.CopyBufferSize
	}
	return largebody.DefaultCopyBufferSize
}

// EffectiveSpoolDir returns SpoolDir if not empty, else a temporary directory fallback.
func (c LargePayloadInput) EffectiveSpoolDir() string {
	return c.SpoolDir
}
