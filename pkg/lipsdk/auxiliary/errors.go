package auxiliary

import "errors"

// ErrNotConfigured means no auxiliary client is bound for this execution snapshot.
var ErrNotConfigured = errors.New("lipsdk/auxiliary: auxiliary client not configured")

// ErrAuxDepthExceeded means nested auxiliary calls exceeded the runtime limit.
var ErrAuxDepthExceeded = errors.New("lipsdk/auxiliary: auxiliary recursion depth exceeded")

// ErrQueueSaturated means the bounded background queue cannot admit another distinct job.
var ErrQueueSaturated = errors.New("lipsdk/auxiliary: background queue saturated")

// ErrAdmissionDenied is reserved for synchronous auxiliary admission (credit/exposure)
// denial at SubmitCollect time. The current in-process scheduler does synchronous
// admission only via the bounded queue (ErrQueueSaturated); asynchronous admission
// failures returned by the runner surface via Await/Poll (PollFailed), not via
// SubmitCollect. Keep this sentinel for future synchronous credit-screen integration
// but do not falsely expose it as a SubmitCollect error today.
var ErrAdmissionDenied = errors.New("lipsdk/auxiliary: admission denied")

// ErrSubmitFailed is a generic non-queue/non-admission submit failure.
// Unknown submit errors are wrapped with this sentinel for typed classification.
var ErrSubmitFailed = errors.New("lipsdk/auxiliary: submit failed")

// ErrResultTooLarge means a background collection exceeded the configured per-job
// or scheduler byte bound before allocation. It is returned via Await/Poll (PollFailed)
// and is inspectable with errors.Is. Zero MaxOutputBytes preserves existing behavior.
var ErrResultTooLarge = errors.New("lipsdk/auxiliary: result too large")
