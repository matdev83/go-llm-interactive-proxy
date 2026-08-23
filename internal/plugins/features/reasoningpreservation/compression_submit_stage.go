package reasoningpreservation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// compressionCoalesceKey derives a versioned content-free digest for SubmitCollect
// coalescing. It contains no reasoning text, credentials, or raw session lineage.
// Version prefix "v1" ensures stable versioning if scheme changes.
// It hashes artifact ID, original/semantic digests, authoritative egress hash,
// policy revision and route with length-prefixed boundaries to avoid collisions.
func compressionCoalesceKey(pr PreparedReservation) string {
	h := sha256.New()
	_, _ = h.Write([]byte("lip.reasoning-preservation.compression.v1"))
	_, _ = h.Write([]byte{0})
	// Length-prefixed strings to avoid boundary collisions.
	for _, v := range []string{pr.Reservation.Correlation.ArtifactID, pr.Reservation.Correlation.PolicyRevision, pr.Route} {
		_, _ = fmt.Fprintf(h, "%d:", len(v))
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	// Fixed-size digests written raw with separators.
	_, _ = h.Write(pr.Reservation.Correlation.OriginalDigest[:])
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(pr.Reservation.Correlation.SemanticDigest[:])
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(pr.EgressPolicyHash[:])
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// NewPostEgressSubmitStage returns a PostEgressStage that builds the detached
// no-tools auxiliary compressor request from the prepared sanitized segments,
// submits it through the generation-bound BackgroundClient with timeout and
// versioned coalesce key, and binds the returned JobID via CAS.
// It never calls Await or Poll. On request-build or submit failure it clears
// only the expected reservation (CAS) and leaves the original intact. On
// accepted JobID it attempts BindCompressionJob; bind failure triggers
// Forget(jobID) and clears only the expected reservation. Empty JobID is
// treated as invalid and clears. Incurred accepted work remains
// accounting-owned (Forget does not cancel billing).
func NewPostEgressSubmitStage(cfg Config, store CompressionStore, svc CompressionServices) PostEgressStage {
	return func(ctx context.Context, pr PreparedReservation) error {
		if store == nil {
			return nil
		}
		if !pr.Reservation.IsReserved() {
			return nil
		}
		if pr.Reservation.ReservationID == "" || pr.Reservation.Correlation.ArtifactID == "" {
			_ = store.ClearCompression(ctx, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.ReservationID)
			return nil
		}
		if len(pr.Segments) == 0 {
			_ = store.ClearCompression(ctx, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.ReservationID)
			return nil
		}
		if isNilCapability(svc.Client) {
			_ = store.ClearCompression(ctx, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.ReservationID)
			return nil
		}
		params := CompressorAuxRequestParams{
			Route:               pr.Route,
			ParentTraceID:       pr.Reservation.Correlation.TraceID,
			ParentALegID:        pr.Reservation.Correlation.ALegID,
			ParentBLegID:        pr.Reservation.Correlation.BLegID,
			ParentBranchBinding: pr.Reservation.Correlation.BranchBinding,
			Segments:            pr.Segments,
			MaxOutputTokens:     cfg.Compression.MaxOutputTokens,
		}
		req, err := BuildCompressorAuxRequest(params)
		if err != nil {
			_ = store.ClearCompression(ctx, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.ReservationID)
			return nil
		}
		coalesceKey := compressionCoalesceKey(pr)
		opts := auxiliary.SubmitOptions{
			Timeout:     cfg.Compression.Timeout,
			CoalesceKey: coalesceKey,
		}
		submitCtx := context.WithoutCancel(ctx)
		submitCtx = scope.WithScope(submitCtx, pr.Reservation.Correlation.Scope)
		jobID, err := svc.Client.SubmitCollect(submitCtx, req, opts)
		if err != nil {
			_ = store.ClearCompression(ctx, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.ReservationID)
			return nil
		}
		if jobID == "" {
			_ = store.ClearCompression(ctx, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.ReservationID)
			return nil
		}
		err = store.BindCompressionJob(ctx, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.ReservationID, jobID, pr.Reservation.Correlation.OriginalDigest, pr.Reservation.Correlation.PolicyRevision)
		if err != nil {
			if !isNilCapability(svc.Client) {
				svc.Client.Forget(jobID)
			}
			_ = store.ClearCompression(ctx, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.ReservationID)
			return nil
		}
		return nil
	}
}
