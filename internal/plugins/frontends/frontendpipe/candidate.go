package frontendpipe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonshape"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
)

// LargePayloadConfig parameterizes the large-payload fast path for a frontend create pipeline
// (Task 7.3, 7.4, Requirements 1, 2, 20; design section 3, 5).
type LargePayloadConfig struct {
	// Enabled gates fast-path candidate evaluation. Default false.
	Enabled bool
	// ThresholdBytes is the decoded-size consideration gate. When <= 0,
	// largebody.DefaultThresholdBytes (1 MiB) is used.
	ThresholdBytes int64
	// WireEligibility optionally supplies the generation-frozen WireEligibilitySummary.
	WireEligibility largebody.WireEligibilitySummary
	// SpoolLedger optionally supplies the shared in-flight logical spool ledger (Task 4.1).
	SpoolLedger *largebody.SpoolLedger
	// MemorySpoolBytes bounds retained bytes in Go heap per capture before spilling.
	MemorySpoolBytes int64
	// SpoolDir is the directory where temporary spill files are created.
	SpoolDir string
	// CopyBufferSize is the chunk size used for reading from the client body.
	CopyBufferSize int
}

// EffectiveThresholdBytes returns ThresholdBytes if > 0, else DefaultThresholdBytes (1 MiB).
func (c LargePayloadConfig) EffectiveThresholdBytes() int64 {
	if c.ThresholdBytes > 0 {
		return c.ThresholdBytes
	}
	return largebody.DefaultThresholdBytes
}

// EffectiveMemorySpoolBytes returns MemorySpoolBytes if > 0, else DefaultMemorySpoolBytes (64 KiB).
func (c LargePayloadConfig) EffectiveMemorySpoolBytes() int64 {
	if c.MemorySpoolBytes > 0 {
		return c.MemorySpoolBytes
	}
	return largebody.DefaultMemorySpoolBytes
}

// EffectiveCopyBufferSize returns CopyBufferSize if > 0, else DefaultCopyBufferSize (32 KiB).
func (c LargePayloadConfig) EffectiveCopyBufferSize() int {
	if c.CopyBufferSize > 0 {
		return c.CopyBufferSize
	}
	return largebody.DefaultCopyBufferSize
}

// StaticDispositionProvider is the optional interface an executor may implement
// to supply an O(1) generation-frozen pre-capture disposition (design section 4, 8; Task 3.6, 7.3).
type StaticDispositionProvider interface {
	LargeBodyStaticDisposition(profileID string) (largebody.StaticWireDisposition, largebody.StaticWireReason)
}

// PreCaptureGate identifies which cheap pre-capture gate made the decision.
type PreCaptureGate uint8

const (
	// PreCaptureGateNone represents an unassigned gate (or passed all gates).
	PreCaptureGateNone PreCaptureGate = iota
	// PreCaptureGateFeatureProfileExecutor is Gate 1: feature, profile, and two-phase executor availability.
	PreCaptureGateFeatureProfileExecutor
	// PreCaptureGateKnownLengthThreshold is Gate 2: known identity/uncompressed request length below threshold.
	PreCaptureGateKnownLengthThreshold
	// PreCaptureGateGzipWave1 is Gate 3: gzip compression in Wave 1.
	PreCaptureGateGzipWave1
	// PreCaptureGateStaticDisposition is Gate 4: frozen static disposition DefinitelyCanonical.
	PreCaptureGateStaticDisposition
	// PreCaptureGateLegacyRouteResolver is Gate 5: legacy full-body ResolveRouteSelector configured.
	PreCaptureGateLegacyRouteResolver
)

func (g PreCaptureGate) String() string {
	switch g {
	case PreCaptureGateFeatureProfileExecutor:
		return "feature_profile_executor"
	case PreCaptureGateKnownLengthThreshold:
		return "known_length_threshold"
	case PreCaptureGateGzipWave1:
		return "gzip_wave_1"
	case PreCaptureGateStaticDisposition:
		return "static_disposition"
	case PreCaptureGateLegacyRouteResolver:
		return "legacy_route_resolver"
	default:
		return "none"
	}
}

// PreCaptureResult carries the outcome of cheap pre-capture candidate evaluation (Task 7.3).
type PreCaptureResult struct {
	// Candidate reports whether all cheap pre-capture gates passed.
	Candidate bool
	// Gate indicates which gate declined or completed evaluation.
	Gate PreCaptureGate
	// Reason reports the bounded decline reason (matches largebody.StaticWireReason).
	Reason largebody.StaticWireReason
	// Executor is the probed LargeBodyExecutor (non-nil when Candidate is true).
	Executor largebody.LargeBodyExecutor
}

// Declined reports whether the request was declined to canonical processing.
func (r PreCaptureResult) Declined() bool {
	return !r.Candidate
}

// EvaluatePreCaptureGates applies cheap pre-capture gates in the exact specification order:
//  1. feature/profile/two-phase executor available;
//  2. parsed known identity/uncompressed request length below threshold => canonical;
//  3. gzip wave 1 => canonical;
//  4. frozen static disposition DefinitelyCanonical => canonical;
//  5. configured legacy full-body ResolveRouteSelector without bounded contract => canonical;
//
// only then allocate capture/scanner state.
// Do not trust compressed Content-Length as decoded length.
// (Requirements 1, 2, 5, 11, 13, 21; design section 2).
func EvaluatePreCaptureGates[Opts any](spec *Spec[Opts], r *http.Request) PreCaptureResult {
	// Gate 1: feature / profile / two-phase executor available.
	if spec == nil || !spec.LargePayload.Enabled {
		return PreCaptureResult{
			Candidate: false,
			Gate:      PreCaptureGateFeatureProfileExecutor,
			Reason:    largebody.StaticWireReasonFeatureDisabled,
		}
	}
	lbe, ok := CandidatePrerequisites(spec)
	if !ok || lbe == nil {
		return PreCaptureResult{
			Candidate: false,
			Gate:      PreCaptureGateFeatureProfileExecutor,
			Reason:    largebody.StaticWireReasonStaticBlocker,
		}
	}

	// Gate 2: parsed known identity/uncompressed request length below threshold => canonical.
	// Do not trust compressed Content-Length as decoded length (Requirements 2.2, 2.7, 11.5).
	isCompressed := isRequestCompressed(r)
	threshold := spec.LargePayload.EffectiveThresholdBytes()
	if !isCompressed && r.ContentLength >= 0 && r.ContentLength < threshold {
		return PreCaptureResult{
			Candidate: false,
			Gate:      PreCaptureGateKnownLengthThreshold,
			Reason:    largebody.StaticWireReasonBelowThreshold,
		}
	}

	// Gate 3: gzip wave 1 => canonical (Requirement 11.1; design section 2).
	if isRequestGzip(r) {
		return PreCaptureResult{
			Candidate: false,
			Gate:      PreCaptureGateGzipWave1,
			Reason:    largebody.StaticWireReasonGzipCompressed,
		}
	}
	if isCompressed {
		return PreCaptureResult{
			Candidate: false,
			Gate:      PreCaptureGateGzipWave1,
			Reason:    largebody.StaticWireReasonGzipCompressed,
		}
	}

	// Gate 4: frozen static disposition DefinitelyCanonical => canonical (Requirements 1.9, 5.9, 5.10, 21.12; design section 4).
	profileID := ""
	if spec.Profile != nil {
		profileID = spec.Profile.ProfileID()
	}
	if sdp, ok := spec.Exec.(StaticDispositionProvider); ok {
		disp, reason := sdp.LargeBodyStaticDisposition(profileID)
		if disp.IsDefinitelyCanonical() {
			if reason == largebody.StaticWireReasonNone {
				reason = largebody.StaticWireReasonStaticBlocker
			}
			return PreCaptureResult{
				Candidate: false,
				Gate:      PreCaptureGateStaticDisposition,
				Reason:    reason,
			}
		}
	}
	if spec.LargePayload.WireEligibility.Sealed() {
		disp, reason := spec.LargePayload.WireEligibility.StaticDisposition(largebody.StaticDispositionInput{
			FeatureEnabled:           spec.LargePayload.Enabled,
			ThresholdBytes:           threshold,
			HasKnownLength:           r.ContentLength >= 0,
			ContentLength:            r.ContentLength,
			GzipCompressed:           isCompressed,
			LegacyResolverConfigured: spec.ResolveRouteSelector != nil,
		})
		if disp.IsDefinitelyCanonical() {
			return PreCaptureResult{
				Candidate: false,
				Gate:      PreCaptureGateStaticDisposition,
				Reason:    reason,
			}
		}
	}

	// Gate 5: configured legacy full-body ResolveRouteSelector without bounded contract => canonical (Requirement 13.2; design section 1, 2).
	if spec.ResolveRouteSelector != nil {
		return PreCaptureResult{
			Candidate: false,
			Gate:      PreCaptureGateLegacyRouteResolver,
			Reason:    largebody.StaticWireReasonLegacyResolverConfigured,
		}
	}

	// All cheap pre-capture gates passed. Eligible to allocate capture/scanner state (Task 7.4).
	return PreCaptureResult{
		Candidate: true,
		Gate:      PreCaptureGateNone,
		Reason:    largebody.StaticWireReasonNone,
		Executor:  lbe,
	}
}

func isRequestGzip(r *http.Request) bool {
	h := strings.TrimSpace(r.Header.Get("Content-Encoding"))
	if h == "" {
		return false
	}
	for part := range strings.SplitSeq(h, ",") {
		if strings.EqualFold(strings.TrimSpace(part), "gzip") {
			return true
		}
	}
	return false
}

func isRequestCompressed(r *http.Request) bool {
	h := strings.TrimSpace(r.Header.Get("Content-Encoding"))
	return h != "" && !strings.EqualFold(h, "identity")
}

// CandidateCaptureResult reports the outcome of candidate request body capture (Task 7.4).
type CandidateCaptureResult struct {
	// Outcome reports whether capture completed, declined, or failed.
	Outcome largebody.CaptureOutcome
	// Source is the completed replay source (non-nil on CaptureOutcomeCompleted).
	Source largebody.Source
	// Completed is the concrete CompletedSource (non-nil on CaptureOutcomeCompleted).
	Completed *largebody.CompletedSource
	// Continuation is the lossless continuation reader (non-nil on CaptureOutcomeDeclined).
	Continuation *largebody.CaptureReader
	// Digest is the replay source integrity digest computed incrementally during capture.
	Digest largebody.SourceDigest
	// BytesRead is the total client body bytes read during capture.
	BytesRead int64
	// ScannerResult carries token and depth facts from the shared streaming JSON scanner.
	ScannerResult jsonshape.Result
	// ScannerErr reports any syntax or limit error from the shared streaming JSON scanner.
	ScannerErr error
	// Err is any underlying error that caused decline or failure.
	Err error
}

// CaptureCandidateBody captures the request body for a candidate request
// while concurrently feeding chunks to the shared streaming JSON scanner (Task 7.4, Requirements 1, 2, 3, 20).
//
// Invariants:
//   - Logical spool reservation: early for known content-length, incremental for unknown/chunked.
//   - Bounded memory + secure temporary file spill via largebody.SpillBuffer.
//   - Shared streaming JSON scanner fed per chunk.
//   - Enforces the same request body ceiling as canonical reqbody path (*http.MaxBytesError).
//   - On EOF, produces CompletedSource with source integrity digest and finishes scanner.
//   - Recoverable decline (budget exhaustion, spill failure, scanner uncertainty)
//     yields a lossless continuation via largebody.CaptureReader.
//   - Unknown/chunked final size below threshold declines to canonical from source.
func CaptureCandidateBody[Opts any](
	ctx context.Context,
	spec *Spec[Opts],
	w http.ResponseWriter,
	r *http.Request,
	maxBytes int64,
) (CandidateCaptureResult, []byte, error) {
	if maxBytes <= 0 {
		maxBytes = reqbody.DefaultMaxBytes
	}

	// 1. Spool reservation accounting (Task 4.1, Requirement 20.4)
	var reservation *largebody.SpoolReservation
	if spec.LargePayload.SpoolLedger != nil {
		if r.ContentLength > 0 {
			res, err := spec.LargePayload.SpoolLedger.Reserve(r.ContentLength)
			if err != nil {
				if largebody.IsSpoolBudgetExhausted(err) {
					cont, cerr := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
						Remaining:      r.Body,
						MaxBytes:       maxBytes,
						ResponseWriter: w,
					})
					if cerr != nil {
						return CandidateCaptureResult{Outcome: largebody.CaptureOutcomeReadError, Err: cerr}, nil, cerr
					}
					body, berr := drainContinuation(cont)
					return CandidateCaptureResult{
						Outcome:      largebody.CaptureOutcomeDeclined,
						Continuation: cont,
						Err:          err,
					}, body, berr
				}
				body, rerr := reqbody.ReadAll(w, r, maxBytes)
				return CandidateCaptureResult{Outcome: largebody.CaptureOutcomeDeclined, Err: err}, body, rerr
			}
			reservation = res
		} else {
			res, err := spec.LargePayload.SpoolLedger.BeginReservation()
			if err != nil {
				body, rerr := reqbody.ReadAll(w, r, maxBytes)
				return CandidateCaptureResult{Outcome: largebody.CaptureOutcomeDeclined, Err: err}, body, rerr
			}
			reservation = res
		}
	}

	// 2. Bounded RAM + secure file spill buffer (Task 4.2, Requirement 20.1, 20.2)
	spillCfg := largebody.SpillConfig{
		SpoolDir:         spec.LargePayload.SpoolDir,
		MemorySpoolBytes: spec.LargePayload.EffectiveMemorySpoolBytes(),
		CopyBufferSize:   spec.LargePayload.EffectiveCopyBufferSize(),
		Reservation:      reservation,
	}
	spill, err := largebody.NewSpillBuffer(spillCfg)
	if err != nil {
		if reservation != nil {
			_ = reservation.Release()
		}
		cont, cerr := largebody.NewCaptureReader(largebody.CaptureReaderConfig{
			Remaining:      r.Body,
			MaxBytes:       maxBytes,
			ResponseWriter: w,
		})
		if cerr != nil {
			return CandidateCaptureResult{Outcome: largebody.CaptureOutcomeReadError, Err: cerr}, nil, cerr
		}
		body, berr := drainContinuation(cont)
		return CandidateCaptureResult{Outcome: largebody.CaptureOutcomeDeclined, Continuation: cont, Err: err}, body, berr
	}

	// 3. Shared streaming JSON scanner (Task 5.1, Requirement 3)
	scannerLimits := jsonshape.Limits{MaxBytes: maxBytes}
	scanner := jsonshape.NewScanner(ctx, scannerLimits)

	// 4. Capture request body with concurrent chunk feeding to scanner
	captureCfg := largebody.CaptureConfig{
		MaxBytes:       maxBytes,
		ResponseWriter: w,
		CopyBufferSize: spec.LargePayload.EffectiveCopyBufferSize(),
		OnChunk: func(chunk []byte) error {
			return scanner.Feed(chunk)
		},
	}
	captureRes := largebody.CaptureRequestBody(r.Body, spill, captureCfg)

	// 5. Evaluate capture outcome
	switch captureRes.Outcome {
	case largebody.CaptureOutcomeLimitExceeded:
		return CandidateCaptureResult{
			Outcome:   largebody.CaptureOutcomeLimitExceeded,
			BytesRead: captureRes.BytesRead,
			Err:       captureRes.Err,
		}, nil, captureRes.Err

	case largebody.CaptureOutcomeReadError:
		return CandidateCaptureResult{
			Outcome:   largebody.CaptureOutcomeReadError,
			BytesRead: captureRes.BytesRead,
			Err:       captureRes.Err,
		}, nil, captureRes.Err

	case largebody.CaptureOutcomeDeclined:
		cont := captureRes.Continuation
		body, berr := drainContinuation(cont)
		return CandidateCaptureResult{
			Outcome:      largebody.CaptureOutcomeDeclined,
			Continuation: cont,
			BytesRead:    captureRes.BytesRead,
			Err:          captureRes.Err,
		}, body, berr

	case largebody.CaptureOutcomeCompleted:
		compSrc, _ := captureRes.Source.(*largebody.CompletedSource)
		scanResult, scanErr := scanner.Finish()
		res := CandidateCaptureResult{
			Outcome:       largebody.CaptureOutcomeCompleted,
			Source:        captureRes.Source,
			Completed:     compSrc,
			Digest:        captureRes.Digest,
			BytesRead:     captureRes.BytesRead,
			ScannerResult: scanResult,
			ScannerErr:    scanErr,
		}
		if scanErr != nil {
			body, berr := readCompletedSource(compSrc)
			return res, body, berr
		}
		threshold := spec.LargePayload.EffectiveThresholdBytes()
		if compSrc != nil && compSrc.Size() < threshold {
			body, berr := readCompletedSource(compSrc)
			return res, body, berr
		}
		body, berr := readCompletedSource(compSrc)
		return res, body, berr

	default:
		body, berr := reqbody.ReadAll(w, r, maxBytes)
		return CandidateCaptureResult{Outcome: largebody.CaptureOutcomeDeclined}, body, berr
	}
}

func captureCandidate[Opts any](
	ctx context.Context,
	spec *Spec[Opts],
	w http.ResponseWriter,
	r *http.Request,
	maxBytes int64,
) ([]byte, error) {
	capRes, body, err := CaptureCandidateBody(ctx, spec, w, r, maxBytes)
	if spec.OnCandidateCapture != nil {
		spec.OnCandidateCapture(r, capRes)
	}
	if capRes.Completed != nil {
		_ = capRes.Completed.Close()
	}
	return body, err
}

func drainContinuation(cont *largebody.CaptureReader) ([]byte, error) {
	if cont == nil {
		return nil, errors.New("frontendpipe: continuation reader is nil")
	}
	defer func() {
		_ = cont.Close()
	}()
	return io.ReadAll(cont)
}

func readCompletedSource(src *largebody.CompletedSource) ([]byte, error) {
	if src == nil {
		return nil, errors.New("frontendpipe: completed source is nil")
	}
	rc, err := src.Open()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rc.Close()
	}()
	return io.ReadAll(rc)
}
