package auxiliary

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// JobID identifies one bounded process-local background auxiliary collection.
// Job IDs do not imply durable task semantics and are only valid for the owning
// process scheduler's retention window.
type JobID string

// SubmitOptions controls admission of a background collection. CoalesceKey must
// identify a committed continuity transaction; empty keys are rejected so a
// pre-open preview cannot become a billable scheduler job.
// OnCoalesced is an optional synchronous observer invoked before return:
// true if an existing job for the key was returned (coalesced), false if newly admitted.
// It is content-free, must not retain references, must not block, and must not
// panic: the scheduler recovers panics from the callback so an SDK consumer
// cannot crash the scheduler. Additive field: old callers with zero value (nil)
// remain source-compatible; new callers may set it.
type SubmitOptions struct {
	CoalesceKey string
	Timeout     time.Duration
	OnCoalesced func(coalesced bool)
}

// BackgroundClient is the narrow asynchronous auxiliary collection surface.
// It deliberately has no callback, arbitrary task, or durable queue API.
type BackgroundClient interface {
	SubmitCollect(ctx context.Context, req Request, opts SubmitOptions) (JobID, error)
	Await(ctx context.Context, id JobID) (lipapi.Collected, error)
	Forget(id JobID)
}

// DisabledBackgroundClient rejects background work when the feature is not
// composed into a process.
type DisabledBackgroundClient struct{}

func (DisabledBackgroundClient) SubmitCollect(context.Context, Request, SubmitOptions) (JobID, error) {
	return "", ErrNotConfigured
}

func (DisabledBackgroundClient) Await(context.Context, JobID) (lipapi.Collected, error) {
	return lipapi.Collected{}, ErrNotConfigured
}

func (DisabledBackgroundClient) Forget(JobID) {}

var _ BackgroundClient = DisabledBackgroundClient{}
