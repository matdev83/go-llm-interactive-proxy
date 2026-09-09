package frontendpipe

import (
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
)

// LargePayloadConfig parameterizes the large-payload fast path for a frontend create pipeline
// (Task 7.3, Requirements 1, 2; design section 3).
type LargePayloadConfig struct {
	// Enabled gates fast-path candidate evaluation. Default false.
	Enabled bool
	// ThresholdBytes is the decoded-size consideration gate. When <= 0,
	// largebody.DefaultThresholdBytes (1 MiB) is used.
	ThresholdBytes int64
	// WireEligibility optionally supplies the generation-frozen WireEligibilitySummary.
	WireEligibility largebody.WireEligibilitySummary
}

// EffectiveThresholdBytes returns ThresholdBytes if > 0, else DefaultThresholdBytes (1 MiB).
func (c LargePayloadConfig) EffectiveThresholdBytes() int64 {
	if c.ThresholdBytes > 0 {
		return c.ThresholdBytes
	}
	return largebody.DefaultThresholdBytes
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
