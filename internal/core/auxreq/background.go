package auxreq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/genpin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

var (
	// ErrQueueFull means the bounded scheduler cannot admit another distinct job.
	ErrQueueFull = errors.New("auxreq: background queue is full")
	// ErrSchedulerClosed means no new work is admitted after process shutdown starts.
	ErrSchedulerClosed = errors.New("auxreq: background scheduler is closed")
	// ErrInvalidCoalesceKey prevents uncommitted or otherwise unkeyed work from
	// entering the billable background scheduler.
	ErrInvalidCoalesceKey = errors.New("auxreq: empty coalescing key")
	// ErrInvalidJobID means Await was given an empty identifier.
	ErrInvalidJobID = errors.New("auxreq: empty job id")
	// ErrJobNotFound means a result was forgotten or evicted from bounded retention.
	ErrJobNotFound = errors.New("auxreq: job result not found")
	// ErrResultTooLarge means collection exceeded the scheduler's result byte bound.
	ErrResultTooLarge = errors.New("auxreq: collected result exceeds configured bound")
)

const (
	defaultBackgroundWorkers     = 1
	defaultBackgroundQueue       = 32
	defaultBackgroundResults     = 128
	defaultBackgroundResultTTL   = 10 * time.Minute
	defaultBackgroundJobTimeout  = time.Minute
	defaultBackgroundResultBytes = 8 << 20
)

// SchedulerConfig bounds all process-local background state. Zero values use
// conservative defaults; negative values are rejected.
type SchedulerConfig struct {
	Workers        int
	QueueCapacity  int
	MaxResults     int
	ResultTTL      time.Duration
	JobTimeout     time.Duration
	MaxResultBytes int
	// Now supplies scheduler bookkeeping time. Production uses time.Now; tests
	// may provide a deterministic clock without sleeping.
	Now func() time.Time
}

// BackgroundSchedulerConfig is an additive name for callers that prefer the
// capability's full name.
type BackgroundSchedulerConfig = SchedulerConfig

type backgroundJob struct {
	id      auxiliary.JobID
	key     string
	req     auxiliary.Request
	runner  ExecutorRunner
	pin     genpin.Pin
	timeout time.Duration
	done    chan struct{}
	release sync.Once

	// The fields below are protected by BackgroundScheduler.mu.
	finished       bool
	forgotten      bool
	result         *lipapi.Collected
	err            error
	completedAt    time.Time
	completedOrder uint64
	scope          scope.PrincipalScopeView
	hasScope       bool
}

// BackgroundScheduler owns a fixed worker pool and a bounded result registry
// for process-scoped auxiliary collection. It is intentionally not a generic
// task runner: jobs contain only a canonical auxiliary request and a captured
// ExecutorRunner.
type BackgroundScheduler struct {
	root   context.Context
	cancel context.CancelFunc
	runner func() ExecutorRunner
	cfg    SchedulerConfig
	queue  chan *backgroundJob

	mu        sync.Mutex
	closed    bool
	jobs      map[auxiliary.JobID]*backgroundJob
	byKey     map[string]auxiliary.JobID
	sequence  atomic.Uint64
	completed atomic.Uint64
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

func (s *BackgroundScheduler) now() time.Time {
	if s != nil && s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

// NewBackgroundScheduler creates a process-owned collector. The runner
// provider is consulted synchronously on each accepted direct submission;
// workers use the captured runner and never acquire a later generation. Use
// BindRunner when a generation snapshot needs an immutable client view.
func NewBackgroundScheduler(root context.Context, runner func() ExecutorRunner, cfg SchedulerConfig) (*BackgroundScheduler, error) {
	if cfg.Workers < 0 || cfg.QueueCapacity < 0 || cfg.MaxResults < 0 || cfg.MaxResultBytes < 0 || cfg.ResultTTL < 0 || cfg.JobTimeout < 0 {
		return nil, fmt.Errorf("auxreq: scheduler bounds must be non-negative")
	}
	if cfg.Workers == 0 {
		cfg.Workers = defaultBackgroundWorkers
	}
	if cfg.QueueCapacity == 0 {
		cfg.QueueCapacity = defaultBackgroundQueue
	}
	if cfg.MaxResults == 0 {
		cfg.MaxResults = defaultBackgroundResults
	}
	if cfg.ResultTTL == 0 {
		cfg.ResultTTL = defaultBackgroundResultTTL
	}
	if cfg.JobTimeout == 0 {
		cfg.JobTimeout = defaultBackgroundJobTimeout
	}
	if cfg.MaxResultBytes == 0 {
		cfg.MaxResultBytes = defaultBackgroundResultBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if root == nil {
		root = context.Background()
	}
	ctx, cancel := context.WithCancel(root)
	s := &BackgroundScheduler{
		root:   ctx,
		cancel: cancel,
		runner: runner,
		cfg:    cfg,
		queue:  make(chan *backgroundJob, cfg.QueueCapacity),
		jobs:   make(map[auxiliary.JobID]*backgroundJob),
		byKey:  make(map[string]auxiliary.JobID),
	}
	s.wg.Add(cfg.Workers)
	for range cfg.Workers {
		go s.worker()
	}
	return s, nil
}

// NewBackgroundClient is a convenience constructor with the SDK capability's
// name. It preserves the concrete scheduler for callers that need Close.
func NewBackgroundClient(root context.Context, runner func() ExecutorRunner, cfg SchedulerConfig) (*BackgroundScheduler, error) {
	return NewBackgroundScheduler(root, runner, cfg)
}

// BindRunner returns a generation-bound view over the process-owned scheduler.
// The view contains no worker, queue, or result state of its own. Its runner is
// immutable for the lifetime of the view; Await and Forget continue to use the
// scheduler's process-owned result registry.
func (s *BackgroundScheduler) BindRunner(runner ExecutorRunner) auxiliary.BackgroundClient {
	return boundBackgroundClient{scheduler: s, runner: runner}
}

type boundBackgroundClient struct {
	scheduler *BackgroundScheduler
	runner    ExecutorRunner
}

func (c boundBackgroundClient) SubmitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	if c.runner == nil {
		return "", auxiliary.ErrNotConfigured
	}
	if c.scheduler == nil {
		return "", ErrSchedulerClosed
	}
	return c.scheduler.submitCollect(ctx, req, opts, c.runner, true)
}

func (c boundBackgroundClient) Await(ctx context.Context, id auxiliary.JobID) (lipapi.Collected, error) {
	if c.scheduler == nil {
		return lipapi.Collected{}, ErrSchedulerClosed
	}
	return c.scheduler.Await(ctx, id)
}

func (c boundBackgroundClient) Forget(id auxiliary.JobID) {
	if c.scheduler != nil {
		c.scheduler.Forget(id)
	}
}

func (c boundBackgroundClient) Poll(ctx context.Context, id auxiliary.JobID) (auxiliary.PollResult, error) {
	if c.scheduler == nil {
		return auxiliary.PollResult{}, ErrSchedulerClosed
	}
	return c.scheduler.Poll(ctx, id)
}

func (s *BackgroundScheduler) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.root.Done():
			s.drainCanceled()
			return
		case job := <-s.queue:
			if job != nil {
				s.run(job)
			}
		}
	}
}

// drainCanceled releases jobs that were still buffered when an externally
// canceled process root stopped admission. Multiple workers may race here;
// each queue item is received once and finish is idempotent.
func (s *BackgroundScheduler) drainCanceled() {
	for {
		select {
		case job := <-s.queue:
			if job != nil {
				s.finish(job, nil, fmt.Errorf("%w: %v", ErrSchedulerClosed, s.root.Err()))
			}
		default:
			return
		}
	}
}

func (s *BackgroundScheduler) run(job *backgroundJob) {
	if err := s.root.Err(); err != nil {
		s.finish(job, nil, fmt.Errorf("%w: %v", ErrSchedulerClosed, err))
		return
	}
	ctx, cancel := context.WithTimeout(s.root, job.timeout)
	defer cancel()
	ctx = workerAttributionContext(ctx, job.req, job.scope, job.hasScope)
	var collected *lipapi.Collected
	var runErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			runErr = fmt.Errorf("auxreq: background runner panic: %v", recovered)
		}
		s.finish(job, collected, runErr)
	}()
	stream, err := (Client{}).streamWithRunner(ctx, job.req, job.runner, false)
	if err == nil {
		var collectedValue lipapi.Collected
		collectedValue, runErr = lipapi.Collect(ctx, stream)
		collected = &collectedValue
		if runErr == nil && estimateCollectedBytes(collected) > s.cfg.MaxResultBytes {
			runErr = ErrResultTooLarge
		}
	} else if stream != nil {
		_ = stream.Close()
		runErr = err
	} else {
		runErr = err
	}
}

func workerAttributionContext(ctx context.Context, req auxiliary.Request, parent scope.PrincipalScopeView, hasScope bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if hasScope {
		parent.Origin = scope.OriginInternal
		if req.ParentTraceID != "" {
			parent.ParentTraceID = scope.Known(req.ParentTraceID)
		}
		ctx = scope.WithScope(ctx, parent)
	}
	return ctx
}

// SubmitCollect synchronously captures the current executor and generation
// pin, then transfers both into the bounded queue. Parent cancellation after
// this handoff does not cancel worker execution.
func (s *BackgroundScheduler) SubmitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return s.submitCollect(ctx, req, opts, nil, false)
}

// submitCollect performs common admission and ownership transfer for direct
// and generation-bound clients. A bound client supplies the runner already;
// direct clients resolve their provider only after the coalescing fast path.
func (s *BackgroundScheduler) submitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions, boundRunner ExecutorRunner, bound bool) (auxiliary.JobID, error) {
	if bound && boundRunner == nil {
		return "", auxiliary.ErrNotConfigured
	}
	if s == nil {
		return "", ErrSchedulerClosed
	}
	if ctx == nil {
		return "", lipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if req.Call == nil {
		return "", fmt.Errorf("auxreq: nil call: %w", lipapi.ErrInvalidCall)
	}
	key := strings.TrimSpace(opts.CoalesceKey)
	if key == "" {
		return "", ErrInvalidCoalesceKey
	}
	// Resolve an existing committed job before touching the runner provider or
	// generation retainer. The second lookup below remains necessary because a
	// concurrent submit may reserve this key after this fast-path unlocks.
	s.mu.Lock()
	s.cleanupLocked(s.now())
	if s.closed {
		s.mu.Unlock()
		return "", ErrSchedulerClosed
	}
	if id, ok := s.byKey[key]; ok {
		s.mu.Unlock()
		safeInvokeOnCoalesced(opts.OnCoalesced, true)
		return id, nil
	}
	s.mu.Unlock()
	run := boundRunner
	if !bound {
		if s.runner == nil {
			return "", auxiliary.ErrNotConfigured
		}
		run = s.runner()
		if run == nil {
			return "", auxiliary.ErrNotConfigured
		}
	}

	var pin genpin.Pin
	if ret, ok := genpin.FromContext(ctx); ok && ret != nil {
		var retained bool
		pin, retained = ret.Retain(genpin.KindAsync)
		if !retained || pin == nil {
			return "", fmt.Errorf("auxreq: runtime generation pin retain failed: %w", lipapi.ErrInvalidCall)
		}
	}
	releaseOnFailure := true
	defer func() {
		if releaseOnFailure && pin != nil {
			pin.Release()
		}
	}()

	request := cloneRequest(req)
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = s.cfg.JobTimeout
	}
	job := &backgroundJob{
		key:     key,
		req:     request,
		runner:  run,
		pin:     pin,
		timeout: timeout,
		done:    make(chan struct{}),
	}
	if parent, ok := scope.ScopeFromContext(ctx); ok {
		job.scope = parent
		job.hasScope = true
	}

	id, admitted, err := s.publishJob(job)
	if admitted {
		releaseOnFailure = false
		safeInvokeOnCoalesced(opts.OnCoalesced, false)
	} else if err == nil && id != "" {
		safeInvokeOnCoalesced(opts.OnCoalesced, true)
	}
	if errors.Is(err, ErrQueueFull) {
		err = fmt.Errorf("%w: %w", auxiliary.ErrQueueSaturated, err)
	} else if err != nil && !errors.Is(err, auxiliary.ErrQueueSaturated) && !errors.Is(err, ErrSchedulerClosed) && !errors.Is(err, auxiliary.ErrNotConfigured) && !errors.Is(err, ErrInvalidCoalesceKey) && !errors.Is(err, lipapi.ErrInvalidCall) && !errors.Is(err, lipapi.ErrNilContext) {
		// Preserve typed taxonomy for unknown submit failures; wrap with ErrSubmitFailed.
		err = fmt.Errorf("%w: %w", auxiliary.ErrSubmitFailed, err)
	}
	return id, err
}

// safeInvokeOnCoalesced invokes the optional coalescing observer outside any lock
// and recovers from panics so an SDK consumer cannot crash the scheduler.
func safeInvokeOnCoalesced(fn func(bool), coalesced bool) {
	if fn == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	fn(coalesced)
}

func (s *BackgroundScheduler) publishJob(job *backgroundJob) (auxiliary.JobID, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(s.now())
	if s.closed {
		return "", false, ErrSchedulerClosed
	}
	if id, ok := s.byKey[job.key]; ok {
		return id, false, nil
	}
	job.id = s.nextID()
	s.jobs[job.id], s.byKey[job.key] = job, job.id
	select {
	case s.queue <- job:
		return job.id, true, nil
	default:
		delete(s.jobs, job.id)
		if s.byKey[job.key] == job.id {
			delete(s.byKey, job.key)
		}
		return "", false, fmt.Errorf("%w: %w", auxiliary.ErrQueueSaturated, ErrQueueFull)
	}
}

func (s *BackgroundScheduler) nextID() auxiliary.JobID {
	return auxiliary.JobID(fmt.Sprintf("aux-%d", s.sequence.Add(1)))
}

func cloneRequest(req auxiliary.Request) auxiliary.Request {
	out := req
	if req.Call != nil {
		call := lipapi.CloneCall(*req.Call)
		out.Call = &call
	}
	if len(req.DisablePlugins) > 0 {
		out.DisablePlugins = append([]string(nil), req.DisablePlugins...)
	}
	return out
}

func (s *BackgroundScheduler) finish(job *backgroundJob, result *lipapi.Collected, err error) {
	if job == nil {
		return
	}
	s.mu.Lock()
	if job.finished {
		s.mu.Unlock()
		return
	}
	job.finished = true
	job.err = err
	job.completedAt = s.now()
	job.completedOrder = s.completed.Add(1)
	if err == nil && result != nil {
		job.result = cloneCollected(result)
	}
	if !job.forgotten {
		s.jobs[job.id] = job
		if err == nil || job.err != nil {
			s.byKey[job.key] = job.id
		}
		s.cleanupLocked(job.completedAt)
	}
	s.mu.Unlock()
	job.releasePin()
	close(job.done)
}

func (j *backgroundJob) releasePin() {
	j.release.Do(func() {
		if j.pin != nil {
			j.pin.Release()
		}
	})
}

// Await waits for a bounded result without inheriting the worker's lifetime.
func (s *BackgroundScheduler) Await(ctx context.Context, id auxiliary.JobID) (out lipapi.Collected, err error) {
	if s == nil {
		return out, ErrSchedulerClosed
	}
	if ctx == nil {
		return out, lipapi.ErrNilContext
	}
	if strings.TrimSpace(string(id)) == "" {
		return out, ErrInvalidJobID
	}
	s.mu.Lock()
	s.cleanupLocked(s.now())
	job, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return out, ErrJobNotFound
	}
	done := job.done
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return out, ctx.Err()
	case <-done:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok = s.jobs[id]
	if !ok || job.forgotten {
		return out, ErrJobNotFound
	}
	if !job.finished {
		return out, errors.New("auxreq: job completion state unavailable")
	}
	if job.err != nil {
		return out, job.err
	}
	if job.result == nil {
		return out, errors.New("auxreq: completed job has no result")
	}
	cloneCollectedInto(&out, job.result)
	return out, nil
}

// Poll inspects a background job without blocking. It distinguishes pending,
// completed, failed, and not-found/expired states, clones completed results
// defensively, and does not consume or forget the job.
func (s *BackgroundScheduler) Poll(ctx context.Context, id auxiliary.JobID) (auxiliary.PollResult, error) {
	if s == nil {
		return auxiliary.PollResult{}, ErrSchedulerClosed
	}
	if ctx == nil {
		return auxiliary.PollResult{}, lipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return auxiliary.PollResult{}, err
	}
	if strings.TrimSpace(string(id)) == "" {
		return auxiliary.PollResult{}, ErrInvalidJobID
	}
	s.mu.Lock()
	s.cleanupLocked(s.now())
	job, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return auxiliary.PollResult{State: auxiliary.PollNotFound}, nil
	}
	finished := job.finished
	jobErr := job.err
	resultPtr := job.result
	s.mu.Unlock()
	if !finished {
		return auxiliary.PollResult{State: auxiliary.PollPending}, nil
	}
	if jobErr != nil {
		return auxiliary.PollResult{State: auxiliary.PollFailed, Err: jobErr}, nil
	}
	if resultPtr == nil {
		return auxiliary.PollResult{State: auxiliary.PollFailed, Err: errors.New("auxreq: completed job has no result")}, nil
	}
	var collected lipapi.Collected
	cloneCollectedInto(&collected, resultPtr)
	return auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: collected}, nil
}

// Forget removes a result (or prevents a pending result from being retained)
// from the bounded registry. Active work still owns its submit-time pin until
// terminal worker completion.
func (s *BackgroundScheduler) Forget(id auxiliary.JobID) {
	if s == nil || strings.TrimSpace(string(id)) == "" {
		return
	}
	s.mu.Lock()
	job, ok := s.jobs[id]
	if ok {
		job.forgotten = true
		delete(s.jobs, id)
		if s.byKey[job.key] == id {
			delete(s.byKey, job.key)
		}
	}
	s.mu.Unlock()
}

// Close stops admission, cancels worker contexts, joins all workers, and
// drains any jobs that were still buffered so every retained pin is released.
func (s *BackgroundScheduler) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait()
		for {
			select {
			case job := <-s.queue:
				if job != nil {
					s.finish(job, nil, ErrSchedulerClosed)
				}
			default:
				s.closeErr = nil
				return
			}
		}
	})
	return s.closeErr
}

func (s *BackgroundScheduler) cleanupLocked(now time.Time) {
	for id, job := range s.jobs {
		if !job.finished || job.completedAt.IsZero() || now.Sub(job.completedAt) < s.cfg.ResultTTL {
			continue
		}
		job.forgotten = true
		delete(s.jobs, id)
		if s.byKey[job.key] == id {
			delete(s.byKey, job.key)
		}
	}
	completed := 0
	for _, job := range s.jobs {
		if job.finished {
			completed++
		}
	}
	for completed > s.cfg.MaxResults {
		var oldestID auxiliary.JobID
		var oldest time.Time
		var oldestOrder uint64
		for id, job := range s.jobs {
			if !job.finished || job.forgotten {
				continue
			}
			if oldestID == "" || job.completedAt.Before(oldest) || (job.completedAt.Equal(oldest) && job.completedOrder < oldestOrder) {
				oldestID, oldest = id, job.completedAt
				oldestOrder = job.completedOrder
			}
		}
		if oldestID == "" {
			return
		}
		job := s.jobs[oldestID]
		job.forgotten = true
		delete(s.jobs, oldestID)
		if s.byKey[job.key] == oldestID {
			delete(s.byKey, job.key)
		}
		completed--
	}
}

func estimateCollectedBytes(c *lipapi.Collected) int {
	if c == nil {
		return 0
	}
	n := c.Text.Len() + c.Reasoning.Len()
	for id, b := range c.ToolArgs {
		n += len(id)
		if b != nil {
			n += b.Len()
		}
	}
	for id, name := range c.ToolNames {
		n += len(id) + len(name)
	}
	for _, warning := range c.Warnings {
		n += len(warning)
	}
	for _, part := range c.AssistantMedia {
		n += len(part.Content)
	}
	return n
}

func cloneCollected(in *lipapi.Collected) *lipapi.Collected {
	if in == nil {
		return nil
	}
	out := &lipapi.Collected{}
	cloneCollectedInto(out, in)
	return out
}

// cloneCollectedInto copies a populated collection into an already allocated
// destination without assigning either Collected value. In particular, this
// never copies a non-zero strings.Builder by value.
func cloneCollectedInto(out, in *lipapi.Collected) {
	if out == nil || in == nil {
		return
	}
	out.Text.Reset()
	out.Reasoning.Reset()
	_, _ = out.Text.WriteString(in.Text.String())
	_, _ = out.Reasoning.WriteString(in.Reasoning.String())
	out.ToolArgs = cloneBuilders(in.ToolArgs)
	out.ToolNames = cloneStringMap(in.ToolNames)
	out.ToolCallOrder = cloneStrings(in.ToolCallOrder)
	out.Warnings = cloneStrings(in.Warnings)
	out.InputTokens = in.InputTokens
	out.OutputTokens = in.OutputTokens
	out.CacheReadTokens = in.CacheReadTokens
	out.CacheWriteTokens = in.CacheWriteTokens
	out.ReasoningTokens = in.ReasoningTokens
	out.TotalTokens = in.TotalTokens
	out.CostNanoUnits = in.CostNanoUnits
	out.Currency = in.Currency
	out.CostSource = in.CostSource
	out.TerminalError = cloneEvent(in.TerminalError)
	out.FinishReceived = in.FinishReceived
	out.FinishReason = in.FinishReason
	if in.AssistantMedia != nil {
		out.AssistantMedia = make([]lipapi.Part, len(in.AssistantMedia))
		for i := range in.AssistantMedia {
			out.AssistantMedia[i] = clonePart(in.AssistantMedia[i])
		}
	}
	if in.ReasoningParts != nil {
		out.ReasoningParts = make([]lipapi.ReasoningPart, len(in.ReasoningParts))
		for i := range in.ReasoningParts {
			if copyPart := cloneReasoningPart(&in.ReasoningParts[i]); copyPart != nil {
				out.ReasoningParts[i] = *copyPart
			}
		}
	}
}

func cloneBuilders(in map[string]*strings.Builder) map[string]*strings.Builder {
	if in == nil {
		return nil
	}
	out := make(map[string]*strings.Builder, len(in))
	for id, builder := range in {
		if builder == nil {
			out[id] = nil
			continue
		}
		copyBuilder := &strings.Builder{}
		_, _ = copyBuilder.WriteString(builder.String())
		out[id] = copyBuilder
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func cloneEvent(in *lipapi.Event) *lipapi.Event {
	if in == nil {
		return nil
	}
	out := *in
	out.Opaque = cloneBytes(in.Opaque)
	out.Reasoning = cloneReasoningPart(in.Reasoning)
	out.Item = cloneItem(in.Item)
	if in.UsageScopes != nil {
		out.UsageScopes = make([]lipapi.ScopedUsageDelta, len(in.UsageScopes))
		copy(out.UsageScopes, in.UsageScopes)
	}
	return &out
}

func cloneReasoningPart(in *lipapi.ReasoningPart) *lipapi.ReasoningPart {
	if in == nil {
		return nil
	}
	out := *in
	out.Opaque = cloneRaw(in.Opaque)
	out.Summary = cloneRaw(in.Summary)
	out.Content = cloneRaw(in.Content)
	out.EncryptedContent = cloneRaw(in.EncryptedContent)
	return &out
}

func clonePart(in lipapi.Part) lipapi.Part {
	out := in
	out.Content = cloneRaw(in.Content)
	out.Reasoning = cloneReasoningPart(in.Reasoning)
	return out
}

func cloneItem(in *lipapi.Item) *lipapi.Item {
	if in == nil {
		return nil
	}
	out := *in
	if in.Content != nil {
		out.Content = make([]lipapi.ContentPart, len(in.Content))
		for i := range in.Content {
			out.Content[i] = cloneContentPart(in.Content[i])
		}
	}
	if in.Reference != nil {
		copyReference := *in.Reference
		out.Reference = &copyReference
	}
	if in.ToolCall != nil {
		copyToolCall := *in.ToolCall
		copyToolCall.Arguments = cloneRaw(in.ToolCall.Arguments)
		out.ToolCall = &copyToolCall
	}
	if in.ToolResult != nil {
		copyToolResult := *in.ToolResult
		if in.ToolResult.Parts != nil {
			copyToolResult.Parts = make([]lipapi.ContentPart, len(in.ToolResult.Parts))
			for i := range in.ToolResult.Parts {
				copyToolResult.Parts[i] = cloneContentPart(in.ToolResult.Parts[i])
			}
		}
		out.ToolResult = &copyToolResult
	}
	if in.Reasoning != nil {
		copyReasoning := *in.Reasoning
		copyReasoning.Reasoning = cloneReasoningPart(in.Reasoning.Reasoning)
		out.Reasoning = &copyReasoning
	}
	if in.Compaction != nil {
		copyCompaction := *in.Compaction
		copyCompaction.Opaque = cloneRaw(in.Compaction.Opaque)
		out.Compaction = &copyCompaction
	}
	if in.Extension != nil {
		copyExtension := *in.Extension
		copyExtension.Data = cloneRaw(in.Extension.Data)
		out.Extension = &copyExtension
	}
	return &out
}

func cloneContentPart(in lipapi.ContentPart) lipapi.ContentPart {
	out := in
	out.Reasoning = cloneReasoningPart(in.Reasoning)
	if in.Annotation != nil {
		copyAnnotation := *in.Annotation
		copyAnnotation.Data = cloneRaw(in.Annotation.Data)
		out.Annotation = &copyAnnotation
	}
	if in.Extension != nil {
		copyExtension := *in.Extension
		copyExtension.Data = cloneRaw(in.Extension.Data)
		out.Extension = &copyExtension
	}
	return out
}

func cloneBytes(in []byte) []byte {
	if in == nil {
		return nil
	}
	out := make([]byte, len(in))
	copy(out, in)
	return out
}

func cloneRaw(in json.RawMessage) json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(json.RawMessage, len(in))
	copy(out, in)
	return out
}

var (
	_ auxiliary.BackgroundClient = (*BackgroundScheduler)(nil)
	_ auxiliary.BackgroundClient = boundBackgroundClient{}
	_ auxiliary.BackgroundPoller = (*BackgroundScheduler)(nil)
	_ auxiliary.BackgroundPoller = boundBackgroundClient{}
)
