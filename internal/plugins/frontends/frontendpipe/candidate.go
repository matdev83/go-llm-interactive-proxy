package frontendpipe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonshape"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/jsonguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// DefaultMaxSemanticFactBytes is the default ceiling on profile-derived facts (256 KiB; Requirement 4, design section 3).
const DefaultMaxSemanticFactBytes int64 = 256 * 1024

// LargePayloadConfig parameterizes the large-payload fast path for a frontend create pipeline
// (Task 7.3, 7.4, Requirements 1, 2, 20; design section 3, 5).
type LargePayloadConfig = httpcontract.LargePayloadInput

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
	for _, h := range r.Header.Values("Content-Encoding") {
		for part := range strings.SplitSeq(h, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "gzip") {
				return true
			}
		}
	}
	return false
}

func isRequestCompressed(r *http.Request) bool {
	for _, h := range r.Header.Values("Content-Encoding") {
		for part := range strings.SplitSeq(h, ",") {
			p := strings.TrimSpace(part)
			if p != "" && !strings.EqualFold(p, "identity") {
				return true
			}
		}
	}
	return false
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
		if compSrc == nil || compSrc.Size() < threshold {
			body, berr := readCompletedSource(compSrc)
			return res, body, berr
		}
		return res, nil, nil

	default:
		body, berr := reqbody.ReadAll(w, r, maxBytes)
		return CandidateCaptureResult{Outcome: largebody.CaptureOutcomeDeclined}, body, berr
	}
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

// replayCandidate performs Task 7.5 candidate decode admission and protocol proof replay.
// Returns (decoded, body, ok, err).
// If ok is false and err is nil, an HTTP error response was already written to w.
// If err is non-nil, an error occurred during decode that should be handled by standard decode error handling.
func replayCandidate[Opts any](
	ctx context.Context,
	spec *Spec[Opts],
	w http.ResponseWriter,
	r *http.Request,
	pm PathMatch,
	capRes CandidateCaptureResult,
) (*Decoded, []byte, bool, error) {
	if capRes.ScannerErr != nil {
		_ = capRes.Completed.Close()
		if jsonguard.Classify(capRes.ScannerErr) == jsonguard.KindCanceled {
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WritePreflightCanceled(w))
			return nil, nil, false, nil
		}
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteInvalidJSON(w))
		return nil, nil, false, nil
	}

	if spec.Exec == nil {
		_ = capRes.Completed.Close()
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteExecutorNotConfigured(w))
		return nil, nil, false, nil
	}

	sel := spec.HTTPHeaders.RouteSelector(r.Header)
	// Task 7.5: Legacy full-body route resolver is NOT invoked here.

	// Task 7.5: Acquire exactly one decode-admission permit after EOF — weight = exact final decoded bytes
	weight := capRes.Completed.Size()
	releaseDecode, ok, aerr := decodeqos.TryAdmit(ctx, spec.DecodeAdmission, weight)
	if d := decodeqos.Decide(ok, aerr); d.Status != 0 {
		_ = capRes.Completed.Close()
		if d.RetryAfter {
			w.Header().Set("Retry-After", decodeqos.RetryAfterSeconds)
		}
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteAdmissionReject(w, d))
		return nil, nil, false, nil
	}

	var released bool
	releaseOnce := func() {
		if !released {
			released = true
			if releaseDecode != nil {
				releaseDecode()
			}
		}
	}
	defer releaseOnce()

	var wireCommitted bool
	defer func() {
		if !wireCommitted {
			_ = capRes.Completed.Close()
		}
	}()

	proofIn := ProofInput{
		Ctx:                  ctx,
		Headers:              r.Header,
		URLPath:              r.URL.Path,
		Path:                 pm,
		RouteSelector:        sel,
		RoutePrefixes:        spec.RoutePrefixes,
		DefaultRouteSelector: spec.DefaultRouteSelector,
		RouteFromBodyModel:   spec.RouteFromBodyModel,
		Source:               capRes.Completed,
		BodyBytes:            capRes.Completed.Size(),
		AnthropicVersion:     strings.TrimSpace(r.Header.Get("anthropic-version")),
	}

	proofStart := time.Now()
	var proofOut ProofOutput
	var proofErr error
	if spec.Profile != nil {
		proofOut, proofErr = spec.Profile.CompileProof(ctx, proofIn)
		if proofErr == nil {
			proofErr = proofOut.Validate(DefaultMaxSemanticFactBytes)
		}
	} else {
		proofErr = errors.New("frontendpipe: profile not configured")
	}
	spec.diagnostics().OnStageDuration("proof", time.Since(proofStart))

	if proofErr == nil {
		spec.diagnostics().OnPipelineStage(largebody.StageProfileProven)
	} else {
		// Decline-reason coarseness note (Task 19.1 review finding 3):
		// All profile proof compilation failures map to DeclineReasonProofUncertain
		// because any validation error at the profile boundary indicates wire equivalence
		// cannot be proven, falling back to canonical parsing.
		spec.diagnostics().OnDecline(largebody.DeclineReasonProofUncertain.String())
	}

	if spec.OnCandidateProof != nil {
		spec.OnCandidateProof(r, CandidateProofResult{
			Output:     proofOut,
			PermitHeld: !released,
			Err:        proofErr,
		})
	}

	// Task 11.9: Call assessment while SAME decode permit remains held (Requirement 6.1, 6.2).
	var assessment largebody.Assessment
	var assessErr error
	if proofErr == nil {
		assessStart := time.Now()
		assessor, aok := largebody.AsLargeBodyAssessor(spec.Exec)
		if !aok || assessor == nil {
			assessErr = errors.New("frontendpipe: large body assessor not configured")
		} else {
			assessment, assessErr = assessor.AssessLargeBody(ctx, proofOut.Proof())
		}
		spec.diagnostics().OnStageDuration("assessment", time.Since(assessStart))

		if assessErr == nil && assessment.Accepted() {
			spec.diagnostics().OnPipelineStage(largebody.StageAssessmentEligible)
		} else {
			if assessErr != nil {
				// Decline-reason coarseness note (Task 19.1 review finding 3):
				// Cancellation/timeout during assessment maps to DeclineReasonCanceled.
				// Other assessor failures (unconfigured assessor, authority reject) map to
				// DeclineReasonAuthorityBlocker because the assessor is the side-effect-free
				// authority blocker for wire execution.
				if errors.Is(assessErr, context.Canceled) || errors.Is(assessErr, context.DeadlineExceeded) || ctx.Err() != nil {
					spec.diagnostics().OnDecline(largebody.DeclineReasonCanceled.String())
				} else {
					spec.diagnostics().OnDecline(largebody.DeclineReasonAuthorityBlocker.String())
				}
			} else {
				spec.diagnostics().OnDecline(assessment.Reason.String())
			}
		}

		if spec.OnCandidateAssessment != nil {
			spec.OnCandidateAssessment(r, CandidateAssessmentResult{
				Assessment: assessment,
				PermitHeld: !released,
				Err:        assessErr,
			})
		}
	}

	// Task 11.9: Accept => release once then commit (Requirement 6.5, 6.6).
	// wireCommitted is set only after ExecuteLargeBody returns so a panic
	// inside execution still runs the deferred completion-source close.
	if proofErr == nil && assessErr == nil && assessment.Accepted() {
		releaseOnce()

		wireExec, wok := spec.Exec.(largebody.LargeBodyWireExecutor)
		if !wok || wireExec == nil {
			if lbe, lok := largebody.AsLargeBodyExecutor(spec.Exec); lok && lbe != nil {
				wireExec = lbe
			}
		}

		isStream := proofOut.Seeds().Stream || proofOut.Proof().Delivery == lipapi.DeliveryModeStreaming

		observer := spec.diagnostics()
		observer.OnPipelineStage(largebody.StageWire)
		observer.OnReplay()
		if assessment.WireRequest.Rewrite.NeedsModelRewrite() || assessment.WireDomain.Rewrite.NeedsModelRewrite() {
			observer.OnRewrite()
		}

		var execRes largebody.ExecutionResult
		var execErr error
		if wireExec != nil {
			execStart := time.Now()
			execRes, execErr = spec.executeLargeBody(ctx, w, wireExec, assessment, capRes.Completed, isStream)
			observer.OnStageDuration("execution", time.Since(execStart))
		} else {
			execErr = errors.New("frontendpipe: wire executor not available for accepted assessment")
		}
		wireCommitted = true

		respCtx := NewResponseContext(proofOut.State, execRes)

		if spec.OnWireCommit != nil {
			spec.OnWireCommit(r, WireCommitResult{
				Assessment:      assessment,
				Result:          execRes,
				ResponseContext: respCtx,
				PermitHeld:      !released,
				Err:             execErr,
			})
		}

		if execErr != nil {
			_ = capRes.Completed.Close()
			out := classifyExecute(spec, execErr)
			if out.Kind == execerr.KindInternalError && spec.Log != nil && out.Err != nil {
				diag.LogError(ctx, spec.Log, "execute large body failed", diag.AttrOpts{}, out.Err)
			}
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteExecuteError(w, out))
			return nil, nil, false, nil
		}

		if execRes.Stream != nil {
			defer func() {
				_ = execRes.Stream.Close()
			}()
		}
		_ = capRes.Completed.Close()

		// Task 14.1: Write sensitive session carrier headers to response (Requirement 14.6, 18.2).
		respCtx.WriteSessionHeaders(w)

		es := execRes.Stream
		if spec.WireWrapStream != nil {
			var wrapErr error
			es, wrapErr = spec.WireWrapStream(ctx, respCtx, es)
			if wrapErr != nil {
				out := classifyExecute(spec, wrapErr)
				if out.Kind == execerr.KindInternalError && spec.Log != nil && out.Err != nil {
					diag.LogError(ctx, spec.Log, "wire stream wrap failed", diag.AttrOpts{CallID: respCtx.CallID()}, out.Err)
				}
				spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteExecuteError(w, out))
				return nil, nil, false, nil
			}
		}

		isStream = respCtx.IsStream()
		var writeErr error
		if isStream {
			if spec.WireWriteStream != nil {
				writeErr = spec.WireWriteStream(ctx, w, respCtx, es)
			}
		} else {
			if spec.WireWriteNonStream != nil {
				writeErr = spec.WireWriteNonStream(ctx, w, respCtx, es)
			}
		}
		if writeErr != nil {
			if spec.Log != nil {
				diag.LogError(ctx, spec.Log, "wire response encode failed", diag.AttrOpts{CallID: respCtx.CallID()}, writeErr)
			}
			if !isStream {
				spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteEncodeFailed(w))
			}
			return nil, nil, false, nil
		}

		if w.Header().Get("Content-Type") == "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
		return nil, nil, false, nil
	}

	// Task 11.9 / Task 7.6: Proof or assessment decline owns same-permit fallback:
	// canonical Spec.Decode from replay under the original permit still held,
	// with no release/reacquire and no second TryAdmit/429/503 decision.
	spec.diagnostics().OnPipelineStage(largebody.StageCanonical)
	body, berr := readCompletedSource(capRes.Completed)
	if berr != nil {
		return nil, nil, false, berr
	}
	if sel == "" && spec.RouteFromBodyModel {
		if proofErr == nil && proofOut.Proof().RouteSelector != "" {
			sel = proofOut.Proof().RouteSelector
		} else {
			sel = spec.RoutePrefixes.FromModelOrDefault(body, spec.DefaultRouteSelector)
		}
	}
	dctx := DecodeContext{
		Ctx:              ctx,
		Body:             body,
		RouteSelector:    sel,
		Headers:          r.Header,
		Path:             pm,
		URLPath:          r.URL.Path,
		AnthropicVersion: strings.TrimSpace(r.Header.Get("anthropic-version")),
	}
	decoded, derr := spec.Decode(dctx)
	releaseOnce()
	if derr != nil {
		return nil, body, false, derr
	}
	return decoded, body, true, nil
}
