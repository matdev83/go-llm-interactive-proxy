package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 7.1 DuplicateOnce / LifecycleOwner architecture gate.
// Role- and structure-aware detector: ResourceLedger (and per-entry stop guards)
// are the sole generation-resource rollback/quiesce/close execution owners.
// Wrappers that retain independent once/closed/state/condition/error-cache around
// the same ledger, or Generation successful-close / close-result caches, are
// rejected only when the type itself is a generation-resource lifecycle owner
// (own Rollback/Quiesce/Close/Discard methods, or method-body provenance that
// delegates those ops to an owned ledger/runtime). Equivalent aggregate phase
// owners without a ResourceLedger field are rejected by method-set + cache shape.
// Function-local and package-global guarded closures that invoke generation
// resource cleanup and escape into the canonical ledger/close graph are
// rejected as function_lifecycle_wrapper (aliases, method values, one/two-hop
// package helpers with callback-parameter provenance, and nested closure
// bodies resolved; not name-blessed).
// Packages are analyzed independently. Aliases, defined types, embeds, and
// nested/split child structs are resolved through a cycle-safe composition
// graph. Legitimate transfer sync, generation refcount/drain state, manager/
// policy diagnostics, process/backend lifecycle, inert aggregates, unrelated
// Retire close provenance, and unrelated once/error usage pass.

type lifeDupFinding struct {
	Pkg    string
	Type   string
	Field  string
	Role   string
	Detail string
}

func (f lifeDupFinding) String() string {
	loc := f.Pkg + "." + f.Type
	if f.Field != "" {
		loc += "." + f.Field
	}
	return fmt.Sprintf("%s role=%s: %s", loc, f.Role, f.Detail)
}

// TestDuplicateOnce_Detector_SyntheticFixtures locks positive evasions and
// negative controls against the role-aware analyzer (not exact-name lists).
func TestDuplicateOnce_Detector_SyntheticFixtures(t *testing.T) {
	t.Parallel()

	t.Run("rejects_renamed_latch_failure_around_ledger", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `
package runtimebundle
import "sync"
type ResourceLedger struct {
	closeOnce sync.Once
	closeErr error
}
`,
			"wrapper.go": `
package runtimebundle
import "sync"
type phaseGuard struct {
	ledger *ResourceLedger
	latch sync.Once
	failure error
}
func (p *phaseGuard) Quiesce() error { return nil }
func (p *phaseGuard) Close() error { return nil }
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "phaseGuard", "wrapper_once_error") {
			t.Fatalf("expected renamed latch+failure wrapper rejection; got %v", got)
		}
	})

	t.Run("rejects_nested_split_ledger_and_guard_children", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `
package runtimebundle
type ResourceLedger struct{}
`,
			"split.go": `
package runtimebundle
import ("context"; "sync")
type ledgerHalf struct {
	ledger *ResourceLedger
}
type guardHalf struct {
	latch sync.Once
	failure error
}
type OuterOwner struct {
	res ledgerHalf
	guard guardHalf
}
func (o *OuterOwner) Rollback(ctx context.Context) error { return nil }
func (o *OuterOwner) Quiesce(ctx context.Context) error { return nil }
func (o *OuterOwner) Close() error {
	if o.res.ledger != nil {
		return nil
	}
	return o.guard.failure
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "OuterOwner", "wrapper_once_error") {
			t.Fatalf("expected split nested ledger+guard rejection; got %v", got)
		}
	})

	t.Run("rejects_nested_state_condition_wrapper", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `
package runtimebundle
type ResourceLedger struct{}
`,
			"nested.go": `
package runtimebundle
import ("context"; "sync")
type ownershipShell struct {
	ledger *ResourceLedger
	mu sync.Mutex
	cond *sync.Cond
	state uint8
	failure error
	quiesceFailure error
}
func (o *ownershipShell) Quiesce(ctx context.Context) error { return o.quiesceFailure }
func (o *ownershipShell) Close() error { return o.failure }
type GenerationBundle struct {
	ownership ownershipShell
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "ownershipShell", "nested_state_condition_cache") {
			t.Fatalf("expected nested state/condition wrapper rejection; got %v", got)
		}
		if lifeDupHasType(got, "GenerationBundle") {
			t.Fatalf("inert parent GenerationBundle must not duplicate finding; got %v", got)
		}
	})

	t.Run("rejects_embedded_and_transitive_alias_defined_types", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `
package runtimebundle
type ResourceLedger struct{}
`,
			"alias.go": `
package runtimebundle
import "sync"
type OnceAlias = sync.Once
type ErrAlias = error
type LedgerPtr = *ResourceLedger
type lifeShell OnceAlias
type Mid struct {
	L LedgerPtr
}
type renamedOwner struct {
	Mid
	once lifeShell
	err ErrAlias
}
func (r *renamedOwner) Quiesce() error { return r.err }
func (r *renamedOwner) Close() error { return r.err }
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "renamedOwner", "wrapper_once_error") &&
			!lifeDupHas(got, "renamedOwner", "defined_once_wrapper") {
			t.Fatalf("expected embedded/alias/defined lifecycle type rejection; got %v", got)
		}
	})

	t.Run("rejects_generation_terminal_failure_close_cache", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"gen.go": `
package runtimehost
import ("sync"; "sync/atomic")
type OwnedCloser interface{ Close() error }
type Generation struct {
	owned OwnedCloser
	terminal atomic.Bool
	failure error
	closeMu sync.Mutex
}
func (g *Generation) Close() error {
	if g.owned != nil {
		return g.owned.Close()
	}
	return g.failure
}
func (g *Generation) Discard() error { return g.Close() }
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "Generation", "generation_close_cache") {
			t.Fatalf("expected Generation terminal+failure rejection; got %v", got)
		}
	})

	t.Run("rejects_condition_state_wrapper_around_ledger", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
`,
			"cond.go": `
package runtimebundle
import ("context"; "sync")
type genLifeState uint8
type generationOwnership struct {
	ledger *ResourceLedger
	mu sync.Mutex
	cond *sync.Cond
	state genLifeState
	quiescing bool
	closing bool
	quiesceFailure error
	closeFailure error
}
func (o *generationOwnership) Quiesce(ctx context.Context) error { return o.quiesceFailure }
func (o *generationOwnership) Close() error { return o.closeFailure }
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "generationOwnership", "nested_state_condition_cache") {
			t.Fatalf("expected condition/state wrapper rejection; got %v", got)
		}
	})

	t.Run("rejects_retirement_collaborator_arbitrary_close_cache", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"worker.go": `
package runtimehost
import ("context"; "sync")
type CleanupPolicy struct{ MaxAttempts int }
type RetirementStatus struct{ Outcome string }
type Generation struct{}
type QuiesceCloser interface{ Close() error; Quiesce(context.Context) error }
type LifecycleWorker struct {
	budget CleanupPolicy
	mu sync.Mutex
	snap RetirementStatus
	finished bool
	finishErr error
}
func (w *LifecycleWorker) Retire(ctx context.Context, gen *Generation, resource QuiesceCloser) error {
	target := resource
	if target != nil {
		closer := target.Close
		_ = closer()
	}
	return w.finishErr
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "LifecycleWorker", "retirement_authoritative_close") {
			t.Fatalf("expected retirement collaborator authoritative-close rejection; got %v", got)
		}
	})

	t.Run("rejects_equivalent_phase_owner_without_ledger_field", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"owner.go": `
package runtimebundle
import ("context"; "sync")
type replacementOwner struct {
	resources []func() error
	handles map[string]func() error
	latch sync.Once
	failure error
	phaseMu sync.Mutex
}
func (r *replacementOwner) Rollback(ctx context.Context) error { return nil }
func (r *replacementOwner) Quiesce(ctx context.Context) error { return nil }
func (r *replacementOwner) Close() error {
	r.latch.Do(func() { r.failure = nil })
	return r.failure
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "replacementOwner", "equivalent_phase_owner") {
			t.Fatalf("expected equivalent phase owner without ResourceLedger rejection; got %v", got)
		}
	})

	t.Run("accepts_plain_canonical_ledger_owner", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `
package runtimebundle
import ("sync"; "sync/atomic")
type ResourceLedger struct {
	mu sync.Mutex
	rollbackOnce sync.Once
	quiesceOnce sync.Once
	closeOnce sync.Once
	closeErr error
	closed atomic.Bool
}
type ledgerEntry struct {
	stopOnce sync.Once
	stopErr error
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHasType(got, "ResourceLedger") || lifeDupHasType(got, "ledgerEntry") {
			t.Fatalf("canonical ledger must not be rejected; got %v", got)
		}
	})

	t.Run("accepts_transfer_only_synchronization", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
`,
			"candidate.go": `
package runtimebundle
import "sync"
type CandidateRuntime struct {
	Ledger *ResourceLedger
	lifeMu sync.Mutex
	ledgerTransferred bool
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHasType(got, "CandidateRuntime") {
			t.Fatalf("transfer-only CandidateRuntime must not be rejected; got %v", got)
		}
	})

	t.Run("accepts_generation_refcount_drain_retry_mutex", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"gen.go": `
package runtimehost
import ("sync"; "sync/atomic")
type OwnedCloser interface{ Close() error }
type Generation struct {
	id atomic.Int64
	word atomic.Uint64
	drainMu sync.Mutex
	drainCh chan struct{}
	drainClosed bool
	retireMu sync.Mutex
	closeMu sync.Mutex
	closeCount atomic.Int32
	owned OwnedCloser
}
func (g *Generation) Close() error { return nil }
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHasType(got, "Generation") {
			t.Fatalf("refcount/drain Generation must not be rejected; got %v", got)
		}
	})

	t.Run("accepts_policy_retry_and_diagnostic_status", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"worker.go": `
package runtimehost
import ("context"; "sync")
type CleanupPolicy struct{ MaxAttempts int }
type RetirementStatus struct{ Outcome string; Attempts int }
type Generation struct{}
type QuiesceCloser interface{ Close() error }
type LifecycleWorker struct {
	budget CleanupPolicy
	statusMu sync.Mutex
	snap RetirementStatus
}
func (w *LifecycleWorker) Retire(ctx context.Context, g *Generation, owned QuiesceCloser) error {
	return nil
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHasType(got, "LifecycleWorker") {
			t.Fatalf("policy/diagnostic LifecycleWorker must not be rejected; got %v", got)
		}
	})

	t.Run("accepts_process_backend_and_unrelated_once_error", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"process.go": `
package runtimebundle
import ("sync"; "sync/atomic")
type ProcessServices struct {
	closeOnce sync.Once
	closeErr error
	closed atomic.Bool
}
func (p *ProcessServices) Close() error { return p.closeErr }
type BackendInstance struct {
	startOnce sync.Once
	startErr error
	closeOnce sync.Once
	closeErr error
	started atomic.Bool
}
func (b *BackendInstance) Start() error { return b.startErr }
func (b *BackendInstance) Close() error { return b.closeErr }
type Unrelated struct {
	once sync.Once
	err error
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		for _, name := range []string{"ProcessServices", "BackendInstance", "Unrelated"} {
			if lifeDupHasType(got, name) {
				t.Fatalf("unrelated process/backend/once must not be rejected; got %v", got)
			}
		}
	})

	t.Run("accepts_inert_aggregate_with_ledger_and_unrelated_telemetry", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `
package runtimebundle
type ResourceLedger struct{}
`,
			"inert.go": `
package runtimebundle
import "sync"
type ledgerBearing struct {
	ledger *ResourceLedger
}
type telemetryHalf struct {
	retry sync.Once
	lastErr error
}
type InertAggregate struct {
	res ledgerBearing
	tel telemetryHalf
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHasType(got, "InertAggregate") || lifeDupHasType(got, "ledgerBearing") || lifeDupHasType(got, "telemetryHalf") {
			t.Fatalf("inert aggregate without lifecycle role must pass; got %v", got)
		}
	})

	t.Run("accepts_backend_instance_like_single_resource_start_close", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"local.go": `
package runtimebundle
import "sync"
type resourceLocal struct {
	startLatch sync.Once
	startFailure error
	stopLatch sync.Once
	stopFailure error
}
func (r *resourceLocal) Start() error { return r.startFailure }
func (r *resourceLocal) Close() error { return r.stopFailure }
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHasType(got, "resourceLocal") {
			t.Fatalf("BackendInstance-like single-resource Start/Close must pass; got %v", got)
		}
	})

	t.Run("accepts_retire_closing_unrelated_resource", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"worker.go": `
package runtimehost
import ("context"; "sync")
type CleanupPolicy struct{ MaxAttempts int }
type RetirementStatus struct{ Outcome string }
type Generation struct{}
type QuiesceCloser interface{ Close() error; Quiesce(context.Context) error }
type LogHandle interface{ Close() error }
type LifecycleWorker struct {
	budget CleanupPolicy
	mu sync.Mutex
	snap RetirementStatus
	traceDone bool
	traceErr error
}
func (w *LifecycleWorker) Retire(ctx context.Context, gen *Generation, resource QuiesceCloser, log LogHandle) error {
	handle := log
	if handle != nil {
		w.traceErr = handle.Close()
		w.traceDone = true
	}
	return w.traceErr
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHasType(got, "LifecycleWorker") {
			t.Fatalf("Retire closing unrelated log/trace handle must pass; got %v", got)
		}
	})

	t.Run("packages_do_not_merge_same_named_aliases", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"bundle_alias.go": `
package runtimebundle
type ResourceLedger struct{}
type Shadow = string
type Holder struct {
	ledger *ResourceLedger
	tag Shadow
}
`,
			"host_alias.go": `
package runtimehost
import "sync"
type Shadow = sync.Once
type Other struct {
	guard Shadow
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHasType(got, "Holder") {
			t.Fatalf("runtimebundle Holder must not inherit runtimehost Once alias; got %v", got)
		}
	})

	// --- function_lifecycle_wrapper positives ---

	t.Run("rejects_renamed_local_guard_cache_closure_registered_with_ledger", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) AddClose(name string, phase int, closeFn func() error) func() error { return closeFn }
`,
			"wire.go": `
package runtimebundle
import "sync"
type res struct{}
func (r *res) Close() error { return nil }
func wireOwned(ledger *ResourceLedger, r *res) {
	var latch sync.Once
	var failure error
	cb := func() error {
		latch.Do(func() { failure = r.Close() })
		return failure
	}
	ledger.AddClose("r", 0, cb)
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "wireOwned", "function_lifecycle_wrapper") {
			t.Fatalf("expected renamed local guard/cache ledger registration rejection; got %v", got)
		}
	})

	t.Run("rejects_ledger_AddClose_method_value_plus_closure_alias", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) AddClose(name string, phase int, closeFn func() error) func() error { return closeFn }
`,
			"wire.go": `
package runtimebundle
import "sync"
type owned struct{}
func (o *owned) CleanupIdleTransports() error { return nil }
func (o *owned) Close() error { return nil }
func wireMethodValue(ledger *ResourceLedger, o *owned) {
	var guard sync.Once
	var cached error
	release := func() error {
		guard.Do(func() {
			_ = o.CleanupIdleTransports()
			cached = o.Close()
		})
		return cached
	}
	register := ledger.AddClose
	fn := release
	_ = register("o", 0, fn)
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "wireMethodValue", "function_lifecycle_wrapper") {
			t.Fatalf("expected AddClose method-value + closure alias rejection; got %v", got)
		}
	})

	t.Run("rejects_one_and_two_hop_package_helper_registration", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) Add(name string, phase int, closeFn func(ctx any) error) {}
func (l *ResourceLedger) AddClose(name string, phase int, closeFn func() error) func() error { return closeFn }
`,
			"helpers.go": `
package runtimebundle
func hop1(ledger *ResourceLedger, fn func() error) {
	ledger.AddClose("h", 0, fn)
}
func hop2(ledger *ResourceLedger, fn func() error) {
	hop1(ledger, fn)
}
`,
			"wire.go": `
package runtimebundle
import "sync"
type unit struct{}
func (u *unit) Close() error { return nil }
func wireOneHop(ledger *ResourceLedger, u *unit) {
	var once sync.Once
	var err error
	cb := func() error {
		once.Do(func() { err = u.Close() })
		return err
	}
	hop1(ledger, cb)
}
func wireTwoHop(ledger *ResourceLedger, u *unit) {
	type Latch = sync.Once
	type Failure = error
	var latch Latch
	var failure Failure
	cb := func() error {
		latch.Do(func() { failure = u.Close() })
		return failure
	}
	hop2(ledger, cb)
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "wireOneHop", "function_lifecycle_wrapper") {
			t.Fatalf("expected one-hop helper registration rejection; got %v", got)
		}
		if !lifeDupHas(got, "wireTwoHop", "function_lifecycle_wrapper") {
			t.Fatalf("expected two-hop helper registration rejection; got %v", got)
		}
	})

	t.Run("rejects_package_global_renamed_guard_cache_closure", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) AddAction(name string, phase int, start, stop func() error) {}
`,
			"globals.go": `
package runtimebundle
import "sync"
type LatchShell = sync.Once
type ErrShell = error
var (
	globalLatch LatchShell
	globalFail  ErrShell
)
type gadget struct{}
func (g *gadget) Quiesce() error { return nil }
func (g *gadget) Close() error { return nil }
func wireGlobal(ledger *ResourceLedger, g *gadget) {
	cb := func() error {
		globalLatch.Do(func() {
			_ = g.Quiesce()
			globalFail = g.Close()
		})
		return globalFail
	}
	ledger.AddAction("g", 0, nil, cb)
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "wireGlobal", "function_lifecycle_wrapper") {
			t.Fatalf("expected package-global guard/cache ledger registration rejection; got %v", got)
		}
	})

	t.Run("rejects_close_hook_assignment_also_ledger_registered", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) AddClose(name string, phase int, closeFn func() error) func() error { return closeFn }
`,
			"wire.go": `
package runtimebundle
import "sync"
type Backend struct {
	Close func() error
}
type inst struct{}
func (i *inst) Close() error { return nil }
func (i *inst) Discard() error { return i.Close() }
func wireCloseHook(ledger *ResourceLedger, i *inst) *Backend {
	var releaseOnce sync.Once
	var releaseErr error
	release := func() error {
		releaseOnce.Do(func() { releaseErr = i.Discard() })
		return releaseErr
	}
	wrapped := &Backend{}
	wrapped.Close = release
	fn := release
	if ledger != nil {
		fn = ledger.AddClose("backend", 0, release)
	}
	wrapped.Close = fn
	return wrapped
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "wireCloseHook", "function_lifecycle_wrapper") {
			t.Fatalf("expected close-hook + ledger registration rejection; got %v", got)
		}
	})

	// --- function_lifecycle_wrapper negatives ---

	t.Run("accepts_unrelated_local_once_error_telemetry_not_registered", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) AddClose(name string, phase int, closeFn func() error) func() error { return closeFn }
`,
			"tel.go": `
package runtimebundle
import "sync"
func recordTelemetry() error {
	var once sync.Once
	var last error
	emit := func() error {
		once.Do(func() { last = nil })
		return last
	}
	return emit()
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHas(got, "recordTelemetry", "function_lifecycle_wrapper") {
			t.Fatalf("unrelated telemetry once/error must pass; got %v", got)
		}
	})

	t.Run("accepts_process_owned_shutdown_closure_not_ledger", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) AddClose(name string, phase int, closeFn func() error) func() error { return closeFn }
`,
			"process.go": `
package runtimebundle
import "sync"
type ProcessServices struct {
	shutdown func() error
}
func (p *ProcessServices) Close() error {
	if p.shutdown != nil {
		return p.shutdown()
	}
	return nil
}
func wireProcess(ps *ProcessServices) {
	var once sync.Once
	var err error
	fn := func() error {
		once.Do(func() { err = nil })
		return err
	}
	ps.shutdown = fn
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHas(got, "wireProcess", "function_lifecycle_wrapper") {
			t.Fatalf("process-only shutdown closure must pass; got %v", got)
		}
		if lifeDupHasType(got, "ProcessServices") {
			t.Fatalf("ProcessServices must remain process-only; got %v", got)
		}
	})

	t.Run("accepts_backend_instance_resource_local_once_fields", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"backend.go": `
package runtimebundle
import "sync"
type BackendInstance struct {
	startOnce sync.Once
	startErr error
	closeOnce sync.Once
	closeErr error
}
func (b *BackendInstance) Start() error {
	b.startOnce.Do(func() { b.startErr = nil })
	return b.startErr
}
func (b *BackendInstance) Close() error {
	b.closeOnce.Do(func() { b.closeErr = nil })
	return b.closeErr
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHasType(got, "BackendInstance") ||
			lifeDupHas(got, "Close", "function_lifecycle_wrapper") ||
			lifeDupHas(got, "Start", "function_lifecycle_wrapper") {
			t.Fatalf("BackendInstance resource-local Start/Close must pass; got %v", got)
		}
	})

	t.Run("accepts_ledger_callback_without_extra_once_error_guard", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) AddClose(name string, phase int, closeFn func() error) func() error { return closeFn }
`,
			"wire.go": `
package runtimebundle
type unit struct{}
func (u *unit) Close() error { return nil }
func wirePlain(ledger *ResourceLedger, u *unit) {
	ledger.AddClose("u", 0, func() error { return u.Close() })
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHas(got, "wirePlain", "function_lifecycle_wrapper") {
			t.Fatalf("plain ledger callback without once/error must pass; got %v", got)
		}
	})

	t.Run("accepts_once_error_closure_without_lifecycle_cleanup", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) AddClose(name string, phase int, closeFn func() error) func() error { return closeFn }
`,
			"wire.go": `
package runtimebundle
import "sync"
func wireNoCleanup(ledger *ResourceLedger) {
	var once sync.Once
	var err error
	cb := func() error {
		once.Do(func() { err = nil })
		return err
	}
	ledger.AddClose("n", 0, cb)
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHas(got, "wireNoCleanup", "function_lifecycle_wrapper") {
			t.Fatalf("once/error without lifecycle cleanup must pass; got %v", got)
		}
	})

	t.Run("accepts_lifecycle_cleanup_callback_without_independent_guard_cache", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) AddClose(name string, phase int, closeFn func() error) func() error { return closeFn }
`,
			"wire.go": `
package runtimebundle
import "sync"
type unit struct{}
func (u *unit) Close() error { return nil }
func wireUnguarded(ledger *ResourceLedger, u *unit) {
	var note sync.Once
	cb := func() error {
		note.Do(func() {})
		return u.Close()
	}
	ledger.AddClose("u", 0, cb)
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHas(got, "wireUnguarded", "function_lifecycle_wrapper") {
			t.Fatalf("lifecycle cleanup without error cache must pass; got %v", got)
		}
	})

	// --- function_lifecycle_wrapper callback-parameter provenance ---

	t.Run("accepts_unrelated_callback_param_when_helper_registers_fixed_canonical", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) AddClose(name string, phase int, closeFn func() error) func() error { return closeFn }
`,
			"helpers.go": `
package runtimebundle
func canonicalCleanup() error { return nil }
func record(fn func() error) { _ = fn() }
func registerFixed(l *ResourceLedger, telemetry func() error) {
	l.AddClose("owned", 0, canonicalCleanup)
	record(telemetry)
}
`,
			"wire.go": `
package runtimebundle
import "sync"
type span struct{}
func (s *span) Close() error { return nil }
func caller(l *ResourceLedger, s *span) {
	var latch sync.Once
	var failure error
	cb := func() error {
		latch.Do(func() { failure = s.Close() })
		return failure
	}
	registerFixed(l, cb)
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if lifeDupHas(got, "caller", "function_lifecycle_wrapper") {
			t.Fatalf("unrelated telemetry param must not be treated as ledger-registered; got %v", got)
		}
	})

	t.Run("accepts_unguarded_position_when_helper_registers_only_one_of_two_callbacks", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) AddClose(name string, phase int, closeFn func() error) func() error { return closeFn }
`,
			"helpers.go": `
package runtimebundle
func registerOneOfTwo(l *ResourceLedger, owned func() error, telemetry func() error) {
	l.AddClose("owned", 0, owned)
	_ = telemetry()
}
`,
			"wire.go": `
package runtimebundle
import "sync"
type unit struct{}
func (u *unit) Close() error { return nil }
func wireRegistered(l *ResourceLedger, u *unit) {
	var once sync.Once
	var err error
	owned := func() error {
		once.Do(func() { err = u.Close() })
		return err
	}
	registerOneOfTwo(l, owned, func() error { return nil })
}
func wireUnregistered(l *ResourceLedger, u *unit) {
	var latch sync.Once
	var failure error
	tel := func() error {
		latch.Do(func() { failure = u.Close() })
		return failure
	}
	registerOneOfTwo(l, func() error { return nil }, tel)
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "wireRegistered", "function_lifecycle_wrapper") {
			t.Fatalf("registered callback param must still be rejected; got %v", got)
		}
		if lifeDupHas(got, "wireUnregistered", "function_lifecycle_wrapper") {
			t.Fatalf("unregistered callback param must pass; got %v", got)
		}
	})

	t.Run("two_hop_forwards_only_registered_callback_param", func(t *testing.T) {
		t.Parallel()
		files := mustParseLifeDupFiles(t, map[string]string{
			"ledger.go": `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) AddClose(name string, phase int, closeFn func() error) func() error { return closeFn }
`,
			"helpers.go": `
package runtimebundle
func logOnly(fn func() error) { _ = fn() }
func hop1Selective(l *ResourceLedger, owned func() error, dropped func() error) {
	l.AddClose("owned", 0, owned)
	logOnly(dropped)
}
func hop2Selective(l *ResourceLedger, owned func() error, dropped func() error) {
	hop1Selective(l, owned, dropped)
}
`,
			"wire.go": `
package runtimebundle
import "sync"
type unit struct{}
func (u *unit) Close() error { return nil }
func wireDirectSelective(l *ResourceLedger, u *unit) {
	var once sync.Once
	var err error
	owned := func() error {
		once.Do(func() { err = u.Close() })
		return err
	}
	l.AddClose("owned", 0, owned)
}
func wireOneHopForwarded(l *ResourceLedger, u *unit) {
	var once sync.Once
	var err error
	owned := func() error {
		once.Do(func() { err = u.Close() })
		return err
	}
	hop1Selective(l, owned, func() error { return nil })
}
func wireOneHopDropped(l *ResourceLedger, u *unit) {
	var latch sync.Once
	var failure error
	dropped := func() error {
		latch.Do(func() { failure = u.Close() })
		return failure
	}
	hop1Selective(l, func() error { return nil }, dropped)
}
func wireTwoHopForwarded(l *ResourceLedger, u *unit) {
	var once sync.Once
	var err error
	owned := func() error {
		once.Do(func() { err = u.Close() })
		return err
	}
	hop2Selective(l, owned, func() error { return nil })
}
func wireTwoHopDropped(l *ResourceLedger, u *unit) {
	var latch sync.Once
	var failure error
	dropped := func() error {
		latch.Do(func() { failure = u.Close() })
		return failure
	}
	hop2Selective(l, func() error { return nil }, dropped)
}
`,
		})
		got := analyzeDuplicateLifecycleOwners(files)
		if !lifeDupHas(got, "wireDirectSelective", "function_lifecycle_wrapper") {
			t.Fatalf("direct registered callback must be rejected; got %v", got)
		}
		if !lifeDupHas(got, "wireOneHopForwarded", "function_lifecycle_wrapper") {
			t.Fatalf("one-hop forwarded registered param must be rejected; got %v", got)
		}
		if !lifeDupHas(got, "wireTwoHopForwarded", "function_lifecycle_wrapper") {
			t.Fatalf("two-hop forwarded registered param must be rejected; got %v", got)
		}
		if lifeDupHas(got, "wireOneHopDropped", "function_lifecycle_wrapper") {
			t.Fatalf("one-hop dropped/logged param must pass; got %v", got)
		}
		if lifeDupHas(got, "wireTwoHopDropped", "function_lifecycle_wrapper") {
			t.Fatalf("two-hop dropped/logged param must pass; got %v", got)
		}
	})
}

// TestDuplicateOnce_ProductionLifecycleOwnersIsRED fails while concrete
// duplicate owners remain in production (Task 7.1 intentional RED).
// Empty findings pass (final target after Tasks 7.2/7.3). Non-empty findings
// fail with type/field/role diagnostics — never a hardcoded unconditional fail.
func TestDuplicateOnce_ProductionLifecycleOwnersIsRED(t *testing.T) {
	t.Parallel()
	files := parseProductionLifecyclePackages(t)
	got := analyzeDuplicateLifecycleOwners(files)
	if len(got) == 0 {
		return
	}
	joined := make([]string, 0, len(got))
	for _, f := range got {
		joined = append(joined, f.String())
	}
	t.Fatalf("duplicate generation-resource lifecycle owners remain (%d findings):\n%s",
		len(got), strings.Join(joined, "\n"))
}

func lifeDupHas(findings []lifeDupFinding, typ, role string) bool {
	for _, f := range findings {
		if f.Type == typ && f.Role == role {
			return true
		}
	}
	return false
}

func lifeDupHasType(findings []lifeDupFinding, typ string) bool {
	for _, f := range findings {
		if f.Type == typ {
			return true
		}
	}
	return false
}

func mustParseLifeDupFiles(t *testing.T, sources map[string]string) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	out := make(map[string]*ast.File, len(sources))
	for name, src := range sources {
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v\n%s", name, err, src)
		}
		out[name] = f
	}
	return out
}

func parseProductionLifecyclePackages(t *testing.T) map[string]*ast.File {
	t.Helper()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "infra", "runtimebundle"),
		filepath.Join(root, "internal", "infra", "runtimehost"),
	}
	fset := token.NewFileSet()
	out := make(map[string]*ast.File)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			rel, _ := filepath.Rel(root, path)
			out[filepath.ToSlash(rel)] = f
		}
	}
	if len(out) == 0 {
		t.Fatal("no production files scanned")
	}
	return out
}

// analyzeDuplicateLifecycleOwners returns role-wise duplicate lifecycle owners.
// Each package is indexed and scored independently so same-named aliases/types
// never merge across runtimebundle and runtimehost.
func analyzeDuplicateLifecycleOwners(files map[string]*ast.File) []lifeDupFinding {
	byPkg := map[string]map[string]*ast.File{}
	for path, file := range files {
		pkg := packageNameOf(file)
		if byPkg[pkg] == nil {
			byPkg[pkg] = map[string]*ast.File{}
		}
		byPkg[pkg][path] = file
	}
	var findings []lifeDupFinding
	for pkg, pkgFiles := range byPkg {
		findings = append(findings, analyzePkgDuplicateLifecycleOwners(pkg, pkgFiles)...)
	}
	return findings
}

type lifePkgIndex struct {
	pkg              string
	files            map[string]*ast.File
	aliases          map[string]string // name -> underlying type string (non-struct)
	structs          map[string]*ast.StructType
	structPath       map[string]string
	ifaceMethods     map[string]map[string]bool
	typeMethods      map[string]map[string]bool
	typeMethodDecls  map[string][]*ast.FuncDecl
	retireCloseTypes map[string]bool // types whose Retire method closes generation-owned resources
	pkgVars          map[string]string
	pkgFuncs         map[string]*ast.FuncDecl // package-level funcs (non-methods)
	funcPath         map[string]string
}

func analyzePkgDuplicateLifecycleOwners(pkg string, files map[string]*ast.File) []lifeDupFinding {
	idx := buildLifePkgIndex(pkg, files)
	var findings []lifeDupFinding
	for name, st := range idx.structs {
		if isCanonicalLedgerName(name) {
			continue
		}
		shape := idx.aggregateStruct(name, st, map[string]bool{})
		role := idx.lifecycleOwnerRole(name)
		path := idx.structPath[name]
		switch {
		case shape.hasLedger && role.isOwner && shape.hasOnce && shape.hasError:
			findings = append(findings, lifeDupFinding{
				Pkg: pkg, Type: name, Field: strings.Join(shape.onceFields, ","),
				Role:   "wrapper_once_error",
				Detail: fmt.Sprintf("%s: once/error phase guard around ledger (%s)", path, shape.summary()),
			})
		case shape.hasLedger && role.isOwner && shape.onceCount >= 2:
			findings = append(findings, lifeDupFinding{
				Pkg: pkg, Type: name, Field: strings.Join(shape.onceFields, ","),
				Role:   "wrapper_once_error",
				Detail: fmt.Sprintf("%s: multiple phase once guards around ledger (%s)", path, shape.summary()),
			})
		case shape.hasLedger && role.isOwner && shape.hasDefinedOnce:
			findings = append(findings, lifeDupFinding{
				Pkg: pkg, Type: name, Field: strings.Join(shape.onceFields, ","),
				Role:   "defined_once_wrapper",
				Detail: fmt.Sprintf("%s: defined/aliased once wrapper around ledger (%s)", path, shape.summary()),
			})
		case shape.hasLedger && role.isOwner && shape.hasMu && shape.hasCond && (shape.hasState || shape.hasError):
			findings = append(findings, lifeDupFinding{
				Pkg: pkg, Type: name, Field: strings.Join(append(shape.stateFields, shape.errFields...), ","),
				Role:   "nested_state_condition_cache",
				Detail: fmt.Sprintf("%s: nested state/condition/error-cache around ledger (%s)", path, shape.summary()),
			})
		case shape.hasLedger && role.isOwner && shape.hasMu && shape.hasState && shape.hasError:
			findings = append(findings, lifeDupFinding{
				Pkg: pkg, Type: name, Field: strings.Join(append(shape.stateFields, shape.errFields...), ","),
				Role:   "nested_state_condition_cache",
				Detail: fmt.Sprintf("%s: nested state/error-cache around ledger (%s)", path, shape.summary()),
			})
		case shape.hasOwnedCloser && role.isOwner && shape.hasTerminalFlag && shape.hasError:
			findings = append(findings, lifeDupFinding{
				Pkg: pkg, Type: name, Field: strings.Join(append(shape.boolFields, shape.errFields...), ","),
				Role:   "generation_close_cache",
				Detail: fmt.Sprintf("%s: successful-close flag and cached resource-close result duplicate canonical runtime/ledger", path),
			})
		case idx.retireCloseTypes[name] && shape.hasTerminalFlag && shape.hasError:
			findings = append(findings, lifeDupFinding{
				Pkg: pkg, Type: name, Field: strings.Join(append(shape.boolFields, shape.errFields...), ","),
				Role:   "retirement_authoritative_close",
				Detail: fmt.Sprintf("%s: retirement collaborator caches authoritative close state", path),
			})
		case !shape.hasLedger && role.phaseCount >= 2 && equivalentPhaseCache(shape):
			findings = append(findings, lifeDupFinding{
				Pkg: pkg, Type: name, Field: strings.Join(append(shape.onceFields, shape.errFields...), ","),
				Role:   "equivalent_phase_owner",
				Detail: fmt.Sprintf("%s: aggregate phase owner without ResourceLedger (%s; phases=%d)", path, shape.summary(), role.phaseCount),
			})
		}
	}
	findings = append(findings, idx.analyzeFunctionLifecycleWrappers()...)
	return findings
}

// analyzeFunctionLifecycleWrappers finds function-local / package-global
// once+error guarded closures that invoke generation-resource cleanup and
// escape into the canonical ResourceLedger registration graph.
func (idx *lifePkgIndex) analyzeFunctionLifecycleWrappers() []lifeDupFinding {
	registrars := idx.ledgerRegistrarCallbackParams()
	var findings []lifeDupFinding
	seen := map[string]bool{}
	for path, file := range idx.files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Body == nil {
				continue
			}
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				recv := strings.TrimPrefix(recvTypeName(fd.Recv.List[0].Type), "*")
				if isCanonicalLedgerName(recv) {
					continue
				}
			}
			for _, f := range idx.scanFuncForLifecycleWrappers(fd, path, registrars) {
				key := f.Type + "|" + f.Field + "|" + f.Role
				if seen[key] {
					continue
				}
				seen[key] = true
				findings = append(findings, f)
			}
		}
	}
	return findings
}

// pkgHelperForward records that a package helper forwards one of its formal
// parameters into a specific argument position of another package helper.
type pkgHelperForward struct {
	callee    string
	ourParam  int
	calleeArg int
}

// ledgerRegistrarCallbackParams summarizes, for each package-local helper, which
// formal parameter indices reach a ResourceLedger callback registration position
// (directly or via one intermediate helper hop). Empty/absent entries mean the
// helper does not register any of its own parameters as callbacks.
func (idx *lifePkgIndex) ledgerRegistrarCallbackParams() map[string]map[int]bool {
	direct := map[string]map[int]bool{}
	forwards := map[string][]pkgHelperForward{}
	for name, fd := range idx.pkgFuncs {
		if fd == nil || fd.Body == nil {
			continue
		}
		env := idx.newFuncLifeEnv(fd)
		idx.seedFuncLifeEnv(fd, env)
		formals := formalParamIndexes(fd)
		for pname := range formals {
			env.aliases[pname] = pname
		}
		// Collect aliases/locals before provenance inspection.
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				idx.noteFuncLifeAssign(x, env)
			case *ast.DeclStmt:
				idx.noteFuncLifeDecl(x, env)
			}
			return true
		})
		registered := map[int]bool{}
		var edges []pkgHelperForward
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if positions := idx.directLedgerCallbackPositions(call, env); len(positions) > 0 {
				for pos := range positions {
					if pos >= len(call.Args) {
						continue
					}
					if pidx, ok := idx.argFormalParamIndex(call.Args[pos], env, formals); ok {
						registered[pidx] = true
					}
				}
			}
			callee, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			if _, ok := idx.pkgFuncs[callee.Name]; !ok {
				return true
			}
			for argIdx, arg := range call.Args {
				if pidx, ok := idx.argFormalParamIndex(arg, env, formals); ok {
					edges = append(edges, pkgHelperForward{
						callee:    callee.Name,
						ourParam:  pidx,
						calleeArg: argIdx,
					})
				}
			}
			return true
		})
		direct[name] = registered
		forwards[name] = edges
	}
	out := map[string]map[int]bool{}
	for name := range idx.pkgFuncs {
		// hops=1: helper may register directly or forward into a direct registrar
		// (two-hop from the original caller).
		params := idx.resolveRegistrarCallbackParams(name, direct, forwards, 1, map[string]bool{})
		if len(params) > 0 {
			out[name] = params
		}
	}
	return out
}

func (idx *lifePkgIndex) resolveRegistrarCallbackParams(
	name string,
	direct map[string]map[int]bool,
	forwards map[string][]pkgHelperForward,
	hops int,
	stack map[string]bool,
) map[int]bool {
	if hops < 0 || stack[name] {
		return nil
	}
	out := map[int]bool{}
	for p := range direct[name] {
		out[p] = true
	}
	stack[name] = true
	defer delete(stack, name)
	for _, edge := range forwards[name] {
		calleeParams := idx.resolveRegistrarCallbackParams(edge.callee, direct, forwards, hops-1, stack)
		if calleeParams[edge.calleeArg] {
			out[edge.ourParam] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func formalParamIndexes(fd *ast.FuncDecl) map[string]int {
	out := map[string]int{}
	if fd == nil || fd.Type == nil || fd.Type.Params == nil {
		return out
	}
	idx := 0
	for _, f := range fd.Type.Params.List {
		if len(f.Names) == 0 {
			idx++
			continue
		}
		for _, n := range f.Names {
			if n != nil {
				out[n.Name] = idx
			}
			idx++
		}
	}
	return out
}

func ledgerMethodCallbackPositions(method string) map[int]bool {
	switch method {
	case "Add", "AddClose":
		return map[int]bool{2: true}
	case "AddAction":
		return map[int]bool{2: true, 3: true}
	default:
		return nil
	}
}

func copyIntBoolMap(in map[int]bool) map[int]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[int]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type funcLifeEnv struct {
	locals     map[string]string
	onceVars   map[string]bool
	errVars    map[string]bool
	closures   map[string]*ast.FuncLit
	aliases    map[string]string       // name -> root name (closure or formal)
	ledgerRegs map[string]map[int]bool // names bound to Add/AddClose/AddAction method values -> callback arg positions
	typeAlias  map[string]string       // function-local type aliases/defined names
}

func (idx *lifePkgIndex) newFuncLifeEnv(fd *ast.FuncDecl) *funcLifeEnv {
	env := &funcLifeEnv{
		locals:     map[string]string{},
		onceVars:   map[string]bool{},
		errVars:    map[string]bool{},
		closures:   map[string]*ast.FuncLit{},
		aliases:    map[string]string{},
		ledgerRegs: map[string]map[int]bool{},
		typeAlias:  map[string]string{},
	}
	for name, typ := range idx.pkgVars {
		resolved := idx.resolve(typ, map[string]bool{})
		env.locals[name] = resolved
		if idx.isOnceLike(resolved) {
			env.onceVars[name] = true
		}
		if idx.isErrorLike(resolved) {
			env.errVars[name] = true
		}
	}
	return env
}

func (idx *lifePkgIndex) resolveInFunc(typ string, env *funcLifeEnv) string {
	if typ == "" {
		return typ
	}
	if next, ok := env.typeAlias[typ]; ok {
		return idx.resolveInFunc(next, env)
	}
	base := strings.TrimPrefix(typ, "*")
	if next, ok := env.typeAlias[base]; ok {
		resolved := idx.resolveInFunc(next, env)
		if strings.HasPrefix(typ, "*") && !strings.HasPrefix(resolved, "*") {
			return "*" + resolved
		}
		return resolved
	}
	return idx.resolve(typ, map[string]bool{})
}

func (idx *lifePkgIndex) seedFuncLifeEnv(fd *ast.FuncDecl, env *funcLifeEnv) {
	bind := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			typ := idx.resolveExpr(f.Type)
			for _, n := range f.Names {
				if n == nil {
					continue
				}
				env.locals[n.Name] = typ
				env.aliases[n.Name] = n.Name
				if idx.isOnceLike(typ) {
					env.onceVars[n.Name] = true
				}
				if idx.isErrorLike(typ) {
					env.errVars[n.Name] = true
				}
			}
		}
	}
	if fd.Recv != nil {
		bind(fd.Recv)
	}
	if fd.Type != nil {
		bind(fd.Type.Params)
	}
}

func (idx *lifePkgIndex) isOnceLike(typ string) bool {
	resolved := idx.resolve(typ, map[string]bool{})
	if isOnceType(resolved) {
		return true
	}
	return idx.isDefinedOnce(typ) || idx.isDefinedOnce(resolved)
}

func (idx *lifePkgIndex) isErrorLike(typ string) bool {
	resolved := idx.resolve(typ, map[string]bool{})
	return isErrorType(resolved) || isErrorType(typ)
}

func (idx *lifePkgIndex) noteFuncLifeDecl(ds *ast.DeclStmt, env *funcLifeEnv) {
	gd, ok := ds.Decl.(*ast.GenDecl)
	if !ok {
		return
	}
	switch gd.Tok {
	case token.TYPE:
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil {
				continue
			}
			env.typeAlias[ts.Name.Name] = lifeTypeString(ts.Type)
		}
	case token.VAR:
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name == nil {
					continue
				}
				typ := ""
				if vs.Type != nil {
					typ = idx.resolveInFunc(lifeTypeString(vs.Type), env)
				} else if i < len(vs.Values) {
					typ = idx.resolveInFunc(lifeTypeStringFromValue(vs.Values[i]), env)
				}
				if typ != "" {
					env.locals[name.Name] = typ
				}
				if idx.isOnceLike(typ) {
					env.onceVars[name.Name] = true
				}
				if idx.isErrorLike(typ) {
					env.errVars[name.Name] = true
				}
				if i < len(vs.Values) {
					idx.bindFuncLifeRHS(name.Name, vs.Values[i], env)
				}
			}
		}
	}
}

func (idx *lifePkgIndex) noteFuncLifeAssign(as *ast.AssignStmt, env *funcLifeEnv) {
	for i := 0; i < len(as.Lhs) && i < len(as.Rhs); i++ {
		id, ok := as.Lhs[i].(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		if as.Tok == token.DEFINE {
			typ := idx.resolveInFunc(lifeTypeStringFromValue(as.Rhs[i]), env)
			if typ == "" {
				// Infer from aliased RHS ident.
				if rid, ok := as.Rhs[i].(*ast.Ident); ok {
					if t, ok := env.locals[rid.Name]; ok {
						typ = t
					}
				}
			}
			if typ != "" {
				env.locals[id.Name] = typ
			}
			if idx.isOnceLike(typ) {
				env.onceVars[id.Name] = true
			}
			if idx.isErrorLike(typ) {
				env.errVars[id.Name] = true
			}
		}
		idx.bindFuncLifeRHS(id.Name, as.Rhs[i], env)
	}
}

func (idx *lifePkgIndex) bindFuncLifeRHS(name string, rhs ast.Expr, env *funcLifeEnv) {
	switch r := rhs.(type) {
	case *ast.FuncLit:
		env.closures[name] = r
		env.aliases[name] = name
	case *ast.Ident:
		if root, ok := env.aliases[r.Name]; ok {
			env.aliases[name] = root
			if fl, ok := env.closures[root]; ok {
				env.closures[name] = fl
			}
		} else if fl, ok := env.closures[r.Name]; ok {
			env.aliases[name] = r.Name
			env.closures[name] = fl
		} else {
			// Straightforward ident alias (including formal-parameter provenance).
			env.aliases[name] = r.Name
		}
		if pos := env.ledgerRegs[r.Name]; len(pos) > 0 {
			env.ledgerRegs[name] = copyIntBoolMap(pos)
		}
	case *ast.SelectorExpr:
		if r.Sel == nil {
			return
		}
		switch r.Sel.Name {
		case "Add", "AddClose", "AddAction":
			if idx.exprIsResourceLedger(r.X, env) {
				env.ledgerRegs[name] = ledgerMethodCallbackPositions(r.Sel.Name)
			}
		}
	}
}

func (idx *lifePkgIndex) exprIsResourceLedger(expr ast.Expr, env *funcLifeEnv) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return isLedgerType(env.locals[t.Name])
	case *ast.ParenExpr:
		return idx.exprIsResourceLedger(t.X, env)
	case *ast.StarExpr:
		return idx.exprIsResourceLedger(t.X, env)
	case *ast.SelectorExpr:
		// ledger field access on an owner is still a ledger value.
		if t.Sel != nil && (t.Sel.Name == "Ledger" || t.Sel.Name == "ledger") {
			return true
		}
		return false
	default:
		return false
	}
}

// directLedgerCallbackPositions returns callback argument indexes for a direct
// ResourceLedger.Add/AddClose/AddAction call or a method-value alias of those.
// Package-helper provenance is handled separately via registrar summaries.
func (idx *lifePkgIndex) directLedgerCallbackPositions(call *ast.CallExpr, env *funcLifeEnv) map[int]bool {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if fun.Sel == nil {
			return nil
		}
		switch fun.Sel.Name {
		case "Add", "AddClose", "AddAction":
			if idx.exprIsResourceLedger(fun.X, env) {
				return ledgerMethodCallbackPositions(fun.Sel.Name)
			}
		}
	case *ast.Ident:
		return copyIntBoolMap(env.ledgerRegs[fun.Name])
	}
	return nil
}

func (idx *lifePkgIndex) callCallbackPositions(call *ast.CallExpr, env *funcLifeEnv, registrars map[string]map[int]bool) map[int]bool {
	if pos := idx.directLedgerCallbackPositions(call, env); len(pos) > 0 {
		return pos
	}
	if id, ok := call.Fun.(*ast.Ident); ok {
		return copyIntBoolMap(registrars[id.Name])
	}
	return nil
}

func (idx *lifePkgIndex) argFormalParamIndex(arg ast.Expr, env *funcLifeEnv, formals map[string]int) (int, bool) {
	id, ok := arg.(*ast.Ident)
	if !ok {
		return 0, false
	}
	name := id.Name
	seen := map[string]bool{}
	for name != "" && !seen[name] {
		seen[name] = true
		if pidx, ok := formals[name]; ok {
			return pidx, true
		}
		next, ok := env.aliases[name]
		if !ok || next == name {
			break
		}
		name = next
	}
	if pidx, ok := formals[name]; ok {
		return pidx, true
	}
	return 0, false
}

func (idx *lifePkgIndex) scanFuncForLifecycleWrappers(fd *ast.FuncDecl, path string, registrars map[string]map[int]bool) []lifeDupFinding {
	env := idx.newFuncLifeEnv(fd)
	idx.seedFuncLifeEnv(fd, env)

	// Collect locals/aliases first so later escape checks see the full env.
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			idx.noteFuncLifeAssign(x, env)
		case *ast.DeclStmt:
			idx.noteFuncLifeDecl(x, env)
		}
		return true
	})

	type cand struct {
		root      string
		fl        *ast.FuncLit
		onceUsed  []string
		errUsed   []string
		anonymous bool
	}
	var cands []cand
	seenRoot := map[string]bool{}

	for name, fl := range env.closures {
		root := name
		if r, ok := env.aliases[name]; ok {
			root = r
		}
		if seenRoot[root] {
			continue
		}
		onceUsed, errUsed := idx.funcLitUsesGuardCache(fl, env)
		if len(onceUsed) == 0 || len(errUsed) == 0 {
			continue
		}
		if !funcLitInvokesLifecycleCleanup(fl) {
			continue
		}
		seenRoot[root] = true
		cands = append(cands, cand{root: root, fl: fl, onceUsed: onceUsed, errUsed: errUsed})
	}

	// Anonymous FuncLit arguments occupying proven ledger callback positions.
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		positions := idx.callCallbackPositions(call, env, registrars)
		if len(positions) == 0 {
			return true
		}
		for pos := range positions {
			if pos >= len(call.Args) {
				continue
			}
			fl, ok := call.Args[pos].(*ast.FuncLit)
			if !ok {
				continue
			}
			onceUsed, errUsed := idx.funcLitUsesGuardCache(fl, env)
			if len(onceUsed) == 0 || len(errUsed) == 0 {
				continue
			}
			if !funcLitInvokesLifecycleCleanup(fl) {
				continue
			}
			cands = append(cands, cand{
				root:      fd.Name.Name + ":anon",
				fl:        fl,
				onceUsed:  onceUsed,
				errUsed:   errUsed,
				anonymous: true,
			})
		}
		return true
	})

	ownerName := fd.Name.Name
	var findings []lifeDupFinding
	for _, c := range cands {
		escapes := false
		if c.anonymous {
			escapes = true
		} else {
			escapes = idx.closureEscapesToLifecycle(fd, c.root, env, registrars)
		}
		if !escapes {
			continue
		}
		fields := append(append([]string{}, c.onceUsed...), c.errUsed...)
		findings = append(findings, lifeDupFinding{
			Pkg:   idx.pkg,
			Type:  ownerName,
			Field: strings.Join(fields, ","),
			Role:  "function_lifecycle_wrapper",
			Detail: fmt.Sprintf("%s: function-local/package-global once/error guard around generation-resource cleanup registered into ledger graph (%s)",
				path, strings.Join(fields, ",")),
		})
	}
	return findings
}

func (idx *lifePkgIndex) funcLitUsesGuardCache(fl *ast.FuncLit, env *funcLifeEnv) (onceUsed, errUsed []string) {
	if fl == nil {
		return nil, nil
	}
	onceSeen := map[string]bool{}
	errSeen := map[string]bool{}
	ast.Inspect(fl, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if env.onceVars[x.Name] && !onceSeen[x.Name] {
				onceSeen[x.Name] = true
				onceUsed = append(onceUsed, x.Name)
			}
			if env.errVars[x.Name] && !errSeen[x.Name] {
				errSeen[x.Name] = true
				errUsed = append(errUsed, x.Name)
			}
		case *ast.SelectorExpr:
			// latch.Do(...) still counts as using the once guard.
			if id, ok := x.X.(*ast.Ident); ok && env.onceVars[id.Name] && x.Sel != nil && x.Sel.Name == "Do" {
				if !onceSeen[id.Name] {
					onceSeen[id.Name] = true
					onceUsed = append(onceUsed, id.Name)
				}
			}
		}
		return true
	})
	return onceUsed, errUsed
}

func funcLitInvokesLifecycleCleanup(fl *ast.FuncLit) bool {
	if fl == nil {
		return false
	}
	found := false
	ast.Inspect(fl, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return true
		}
		switch sel.Sel.Name {
		case "Close", "Quiesce", "Rollback", "Discard", "CleanupIdleTransports":
			found = true
			return false
		}
		return true
	})
	return found
}

func (idx *lifePkgIndex) closureEscapesToLifecycle(fd *ast.FuncDecl, root string, env *funcLifeEnv, registrars map[string]map[int]bool) bool {
	names := map[string]bool{root: true}
	for alias, r := range env.aliases {
		if r == root {
			names[alias] = true
		}
	}
	argMatches := func(arg ast.Expr) bool {
		id, ok := arg.(*ast.Ident)
		return ok && names[id.Name]
	}
	registered := false
	assignedClose := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			positions := idx.callCallbackPositions(x, env, registrars)
			if len(positions) == 0 {
				return true
			}
			for pos := range positions {
				if pos < len(x.Args) && argMatches(x.Args[pos]) {
					registered = true
					return false
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "Close" {
					continue
				}
				if i < len(x.Rhs) {
					if id, ok := x.Rhs[i].(*ast.Ident); ok && names[id.Name] {
						assignedClose = true
					}
				}
			}
		case *ast.ReturnStmt:
			for _, res := range x.Results {
				if id, ok := res.(*ast.Ident); ok && names[id.Name] {
					// Returned only counts when also ledger-registered in this body.
					// Detected via registered flag on the same pass; mark soft escape
					// by checking subsequent/prior registration below.
					_ = id
				}
			}
		}
		return true
	})
	if registered {
		return true
	}
	// Close hook assignment that is also ledger-owned: require registration of
	// the same closure (or its alias) elsewhere in the function.
	if assignedClose {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			positions := idx.callCallbackPositions(call, env, registrars)
			if len(positions) == 0 {
				return true
			}
			for pos := range positions {
				if pos < len(call.Args) && argMatches(call.Args[pos]) {
					registered = true
					return false
				}
			}
			return true
		})
		return registered
	}
	return false
}

func equivalentPhaseCache(shape lifeAggShape) bool {
	if shape.hasOnce && shape.hasError {
		return true
	}
	if shape.hasMu && shape.hasCond && (shape.hasState || shape.hasError) {
		return true
	}
	if shape.hasMu && shape.hasState && shape.hasError {
		return true
	}
	return false
}

type lifeOwnerRole struct {
	isOwner    bool
	phaseCount int
}

func (idx *lifePkgIndex) lifecycleOwnerRole(typeName string) lifeOwnerRole {
	var role lifeOwnerRole
	methods := idx.typeMethods[typeName]
	for _, name := range []string{"Rollback", "Quiesce", "Close", "Discard"} {
		if methods[name] {
			role.phaseCount++
			role.isOwner = true
		}
	}
	if !role.isOwner && idx.methodBodiesDelegateLifecycle(typeName) {
		role.isOwner = true
	}
	return role
}

func (idx *lifePkgIndex) methodBodiesDelegateLifecycle(typeName string) bool {
	for _, fd := range idx.typeMethodDecls[typeName] {
		if fd == nil || fd.Body == nil {
			continue
		}
		found := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			switch sel.Sel.Name {
			case "Rollback", "Quiesce", "Close", "Discard":
				if idx.exprTouchesOwnedLedgerOrRuntime(sel.X, typeName) {
					found = true
					return false
				}
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

func (idx *lifePkgIndex) exprTouchesOwnedLedgerOrRuntime(expr ast.Expr, recvType string) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		// Bare ident is not a field of the receiver; ignore.
		return false
	case *ast.ParenExpr:
		return idx.exprTouchesOwnedLedgerOrRuntime(t.X, recvType)
	case *ast.StarExpr:
		return idx.exprTouchesOwnedLedgerOrRuntime(t.X, recvType)
	case *ast.SelectorExpr:
		if t.Sel == nil {
			return false
		}
		resolved := ""
		if id, ok := t.X.(*ast.Ident); ok && (id.Name == "o" || id.Name == "r" || id.Name == "c" ||
			id.Name == "b" || id.Name == "g" || id.Name == "p" || id.Name == "s" || id.Name == "w") {
			// Receiver field selection: look up field type on recvType.
			resolved = idx.fieldTypeOf(recvType, t.Sel.Name)
		} else if id, ok := t.X.(*ast.Ident); ok {
			// Still try as field on receiver for any short recv name.
			resolved = idx.fieldTypeOf(recvType, t.Sel.Name)
			_ = id
		} else {
			// Nested: o.res.ledger.Close — recurse into X then check Sel.
			if idx.exprTouchesOwnedLedgerOrRuntime(t.X, recvType) {
				return true
			}
			// Or X is receiver-ish selector ending at a field.
			if sel, ok := t.X.(*ast.SelectorExpr); ok && sel.Sel != nil {
				resolved = idx.fieldTypeOf(recvType, sel.Sel.Name)
				if resolved == "" {
					// Try deepest field name as nested type field.
					base := idx.nestedFieldType(recvType, sel)
					resolved = base
				}
			}
		}
		if resolved == "" {
			// Nested field path: walk struct fields.
			resolved = idx.nestedFieldType(recvType, t)
		}
		return isLedgerType(resolved) || idx.isOwnedCloserType(resolved) || isGenerationRuntimeType(resolved)
	default:
		return false
	}
}

func (idx *lifePkgIndex) fieldTypeOf(typeName, field string) string {
	st := idx.structs[typeName]
	if st == nil || st.Fields == nil {
		return ""
	}
	for _, f := range st.Fields.List {
		names := fieldNames(f)
		if len(names) == 0 {
			names = []string{embeddedName(f.Type)}
		}
		for _, n := range names {
			if n == field {
				return idx.resolveExpr(f.Type)
			}
		}
	}
	return ""
}

func (idx *lifePkgIndex) nestedFieldType(recvType string, sel *ast.SelectorExpr) string {
	if sel == nil || sel.Sel == nil {
		return ""
	}
	// Collect path of field names from outermost receiver to sel.
	var parts []string
	var cur ast.Expr = sel
	for {
		s, ok := cur.(*ast.SelectorExpr)
		if !ok || s.Sel == nil {
			break
		}
		parts = append([]string{s.Sel.Name}, parts...)
		cur = s.X
	}
	if len(parts) == 0 {
		return ""
	}
	typ := recvType
	for _, part := range parts {
		next := idx.fieldTypeOf(typ, part)
		if next == "" {
			// Try nested struct composition by type name.
			base := strings.TrimPrefix(typ, "*")
			if idx.structs[base] != nil {
				next = idx.fieldTypeOf(base, part)
			}
		}
		if next == "" {
			return ""
		}
		typ = next
	}
	return typ
}

func buildLifePkgIndex(pkg string, files map[string]*ast.File) *lifePkgIndex {
	idx := &lifePkgIndex{
		pkg:              pkg,
		files:            files,
		aliases:          map[string]string{},
		structs:          map[string]*ast.StructType{},
		structPath:       map[string]string{},
		ifaceMethods:     map[string]map[string]bool{},
		typeMethods:      map[string]map[string]bool{},
		typeMethodDecls:  map[string][]*ast.FuncDecl{},
		retireCloseTypes: map[string]bool{},
		pkgVars:          map[string]string{},
		pkgFuncs:         map[string]*ast.FuncDecl{},
		funcPath:         map[string]string{},
	}
	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch gd.Tok {
			case token.TYPE:
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					name := ts.Name.Name
					switch typ := ts.Type.(type) {
					case *ast.StructType:
						idx.structs[name] = typ
						idx.structPath[name] = path
					case *ast.InterfaceType:
						idx.ifaceMethods[name] = interfaceMethodSet(typ)
					default:
						idx.aliases[name] = lifeTypeString(ts.Type)
					}
				}
			case token.VAR:
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if name == nil {
							continue
						}
						typ := ""
						if vs.Type != nil {
							typ = lifeTypeString(vs.Type)
						} else if i < len(vs.Values) {
							typ = lifeTypeStringFromValue(vs.Values[i])
						}
						if typ != "" {
							idx.pkgVars[name.Name] = typ
						}
					}
				}
			}
		}
	}
	// Resolve aliases transitively within this package only.
	for i := 0; i < 8; i++ {
		changed := false
		for k, v := range idx.aliases {
			base := strings.TrimPrefix(v, "*")
			if next, ok := idx.aliases[base]; ok {
				if strings.HasPrefix(v, "*") && !strings.HasPrefix(next, "*") {
					next = "*" + next
				}
				if next != v {
					idx.aliases[k] = next
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	for name, typ := range idx.pkgVars {
		idx.pkgVars[name] = idx.resolve(typ, map[string]bool{})
	}
	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil {
				continue
			}
			if fd.Recv == nil || len(fd.Recv.List) == 0 {
				idx.pkgFuncs[fd.Name.Name] = fd
				idx.funcPath[fd.Name.Name] = path
				continue
			}
			recv := strings.TrimPrefix(recvTypeName(fd.Recv.List[0].Type), "*")
			if idx.typeMethods[recv] == nil {
				idx.typeMethods[recv] = map[string]bool{}
			}
			idx.typeMethods[recv][fd.Name.Name] = true
			idx.typeMethodDecls[recv] = append(idx.typeMethodDecls[recv], fd)
			if fd.Name.Name == "Retire" && idx.funcBodyClosesOwnedGeneration(fd) {
				idx.retireCloseTypes[recv] = true
			}
		}
	}
	return idx
}

func lifeTypeStringFromValue(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.CompositeLit:
		return lifeTypeString(t.Type)
	case *ast.UnaryExpr:
		if t.Op == token.AND {
			return "*" + lifeTypeStringFromValue(t.X)
		}
	case *ast.CallExpr:
		if len(t.Args) == 1 {
			if id, ok := t.Fun.(*ast.Ident); ok && (id.Name == "new" || id.Name == "make") {
				return lifeTypeString(t.Args[0])
			}
		}
		return lifeTypeString(t.Fun)
	case *ast.FuncLit:
		return "func"
	case *ast.Ident, *ast.SelectorExpr, *ast.StarExpr:
		return lifeTypeString(t)
	}
	return ""
}

type lifeAggShape struct {
	hasLedger       bool
	hasOnce         bool
	hasDefinedOnce  bool
	onceCount       int
	hasError        bool
	hasMu           bool
	hasCond         bool
	hasState        bool
	hasOwnedCloser  bool
	hasDrainChan    bool
	hasTerminalFlag bool
	onceFields      []string
	errFields       []string
	stateFields     []string
	boolFields      []string
	bits            []string
}

func (s lifeAggShape) summary() string {
	return strings.Join(s.bits, ",")
}

func (idx *lifePkgIndex) aggregateStruct(typeName string, st *ast.StructType, visiting map[string]bool) lifeAggShape {
	var s lifeAggShape
	if st == nil || st.Fields == nil {
		return s
	}
	if visiting[typeName] {
		return s
	}
	visiting[typeName] = true
	defer delete(visiting, typeName)

	for _, f := range st.Fields.List {
		resolved := idx.resolveExpr(f.Type)
		names := fieldNames(f)
		if len(names) == 0 {
			names = []string{embeddedName(f.Type)}
		}
		for _, n := range names {
			idx.contributeField(&s, n, resolved, visiting)
		}
	}
	// Terminal close cache: bool/atomic.Bool that is not solely drain-channel state.
	if s.hasError && len(s.boolFields) > 0 {
		if s.hasDrainChan && !s.hasOwnedCloser {
			// process-local or unrelated
		} else if s.hasOwnedCloser || s.hasLedger || idx.retireCloseTypes[typeName] {
			s.hasTerminalFlag = true
		}
	}
	if s.hasOwnedCloser && s.hasError && len(s.boolFields) > 0 {
		s.hasTerminalFlag = true
	}
	if idx.retireCloseTypes[typeName] && s.hasError && len(s.boolFields) > 0 {
		s.hasTerminalFlag = true
	}
	return s
}

func (idx *lifePkgIndex) contributeField(s *lifeAggShape, name, resolved string, visiting map[string]bool) {
	base := strings.TrimPrefix(resolved, "*")
	switch {
	case isLedgerType(resolved):
		s.hasLedger = true
		s.bits = append(s.bits, "ledger:"+name)
	case isOnceType(resolved):
		s.hasOnce = true
		s.onceCount++
		s.onceFields = append(s.onceFields, name)
		s.bits = append(s.bits, "once:"+name)
	case idx.isDefinedOnce(resolved):
		s.hasOnce = true
		s.hasDefinedOnce = true
		s.onceCount++
		s.onceFields = append(s.onceFields, name)
		s.bits = append(s.bits, "defined_once:"+name)
	case isErrorType(resolved):
		s.hasError = true
		s.errFields = append(s.errFields, name)
		s.bits = append(s.bits, "err:"+name)
	case isCondType(resolved):
		s.hasCond = true
		s.bits = append(s.bits, "cond:"+name)
	case isMutexType(resolved):
		s.hasMu = true
	case isBoolOrAtomicBool(resolved):
		s.boolFields = append(s.boolFields, name)
		s.bits = append(s.bits, "bool:"+name)
	case isDrainChanType(resolved):
		s.hasDrainChan = true
		s.bits = append(s.bits, "drain:"+name)
	case idx.isOwnedCloserType(resolved):
		s.hasOwnedCloser = true
		s.bits = append(s.bits, "owned:"+name)
	case idx.isStateType(resolved, name):
		s.hasState = true
		s.stateFields = append(s.stateFields, name)
		s.bits = append(s.bits, "state:"+name)
	case idx.structs[base] != nil && !isIndependentLifePeer(base):
		// Composition only: flatten nested/split/embedded ownership shells.
		// Do not inherit Generation/Lease/Pin fields through association pointers.
		// Do not inherit receiver-root lifecycle role from nested types.
		nested := idx.aggregateStruct(base, idx.structs[base], visiting)
		s.merge(nested)
	}
}

func (s *lifeAggShape) merge(o lifeAggShape) {
	s.hasLedger = s.hasLedger || o.hasLedger
	s.hasOnce = s.hasOnce || o.hasOnce
	s.hasDefinedOnce = s.hasDefinedOnce || o.hasDefinedOnce
	s.onceCount += o.onceCount
	s.hasError = s.hasError || o.hasError
	s.hasMu = s.hasMu || o.hasMu
	s.hasCond = s.hasCond || o.hasCond
	s.hasState = s.hasState || o.hasState
	s.hasOwnedCloser = s.hasOwnedCloser || o.hasOwnedCloser
	s.hasDrainChan = s.hasDrainChan || o.hasDrainChan
	s.hasTerminalFlag = s.hasTerminalFlag || o.hasTerminalFlag
	s.onceFields = append(s.onceFields, o.onceFields...)
	s.errFields = append(s.errFields, o.errFields...)
	s.stateFields = append(s.stateFields, o.stateFields...)
	s.boolFields = append(s.boolFields, o.boolFields...)
	s.bits = append(s.bits, o.bits...)
}

func (idx *lifePkgIndex) resolveExpr(expr ast.Expr) string {
	return idx.resolve(lifeTypeString(expr), map[string]bool{})
}

func (idx *lifePkgIndex) resolve(typ string, stack map[string]bool) string {
	if typ == "" || stack[typ] {
		return typ
	}
	stack[typ] = true
	defer delete(stack, typ)
	if next, ok := idx.aliases[typ]; ok {
		return idx.resolve(next, stack)
	}
	if strings.HasPrefix(typ, "*") {
		base := strings.TrimPrefix(typ, "*")
		if next, ok := idx.aliases[base]; ok {
			resolved := idx.resolve(next, stack)
			if strings.HasPrefix(resolved, "*") {
				return resolved
			}
			return "*" + resolved
		}
	}
	return typ
}

func (idx *lifePkgIndex) isDefinedOnce(typ string) bool {
	resolved := idx.resolve(typ, map[string]bool{})
	if isOnceType(resolved) {
		return true
	}
	base := strings.TrimPrefix(typ, "*")
	if next, ok := idx.aliases[base]; ok {
		return isOnceType(idx.resolve(next, map[string]bool{}))
	}
	return false
}

func (idx *lifePkgIndex) isOwnedCloserType(typ string) bool {
	t := strings.TrimPrefix(typ, "*")
	switch t {
	case "OwnedCloser", "QuiesceCloser", "PublishedRequestPlane", "GenerationBundle", "GenerationRuntime":
		return true
	}
	if strings.HasSuffix(t, ".OwnedCloser") || strings.HasSuffix(t, ".QuiesceCloser") ||
		strings.HasSuffix(t, ".PublishedRequestPlane") || strings.HasSuffix(t, "GenerationBundle") {
		return true
	}
	if methods, ok := idx.ifaceMethods[t]; ok {
		// Named generation close contracts only — not arbitrary io.Closer lookalikes.
		if t == "OwnedCloser" || t == "QuiesceCloser" || t == "PublishedRequestPlane" {
			return methods["Close"]
		}
	}
	if methods, ok := idx.typeMethods[t]; ok {
		if methods["Close"] || methods["Quiesce"] || methods["Discard"] || methods["Rollback"] {
			if t == "GenerationBundle" || t == "GenerationRuntime" {
				return true
			}
		}
	}
	return false
}

func (idx *lifePkgIndex) isStateType(typ, fieldName string) bool {
	t := strings.TrimPrefix(typ, "*")
	if _, ok := idx.aliases[t]; ok {
		t = strings.TrimPrefix(idx.resolve(t, map[string]bool{}), "*")
	}
	lt := strings.ToLower(t)
	if strings.Contains(lt, "lifestate") || strings.Contains(lt, "genlifestate") {
		return true
	}
	fn := strings.ToLower(fieldName)
	if fn != "state" && !strings.HasSuffix(fn, "state") {
		return false
	}
	switch t {
	case "uint8", "uint16", "uint32", "int8", "int16", "int32":
		return true
	default:
		return false
	}
}

// isIndependentLifePeer names types that associate with lifecycle owners but must
// not be flattened into parents (leases/pins/bindings pointing at Generation).
func isIndependentLifePeer(name string) bool {
	switch name {
	case "Generation", "Manager", "Lease", "Pin", "RequestBinding",
		"ProcessServices", "BackendInstance", "LifecycleWorker", "Host",
		"CandidateRuntime", "Coordinator", "ResourceLedger", "ledgerEntry",
		"pinnedEventStream", "genpinPin":
		return true
	default:
		return false
	}
}

func interfaceMethodSet(it *ast.InterfaceType) map[string]bool {
	out := map[string]bool{}
	if it == nil || it.Methods == nil {
		return out
	}
	for _, m := range it.Methods.List {
		for _, n := range m.Names {
			out[n.Name] = true
		}
	}
	return out
}

// funcBodyClosesOwnedGeneration reports whether Retire closes a generation-owned
// resource. Provenance is type-based (params/local aliases/method values), not
// parameter-name blessed. Unrelated io.Closer / log / file / span closes do not count.
func (idx *lifePkgIndex) funcBodyClosesOwnedGeneration(fd *ast.FuncDecl) bool {
	if fd == nil || fd.Body == nil {
		return false
	}
	locals := map[string]string{}
	closeMethods := map[string]bool{} // local names bound to Close/BeginClose/Discard method values

	bindParams := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, p := range fl.List {
			typ := idx.resolveExpr(p.Type)
			for _, n := range p.Names {
				if n != nil {
					locals[n.Name] = typ
				}
			}
		}
	}
	if fd.Type != nil {
		bindParams(fd.Type.Params)
	}

	recordAlias := func(lhs, rhs ast.Expr) {
		id, ok := lhs.(*ast.Ident)
		if !ok || id.Name == "_" {
			return
		}
		switch r := rhs.(type) {
		case *ast.Ident:
			if typ, ok := locals[r.Name]; ok {
				locals[id.Name] = typ
			}
			if closeMethods[r.Name] {
				closeMethods[id.Name] = true
			}
		case *ast.SelectorExpr:
			if r.Sel == nil {
				return
			}
			switch r.Sel.Name {
			case "Close", "BeginClose", "Discard":
				if idx.exprHasGenerationCloseProvenance(r.X, locals) {
					closeMethods[id.Name] = true
				}
			default:
				// plane := gen.RequestPlane() style is not followed (needs return types).
			}
		}
	}

	found := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			for i := 0; i < len(stmt.Lhs) && i < len(stmt.Rhs); i++ {
				recordAlias(stmt.Lhs[i], stmt.Rhs[i])
			}
		case *ast.DeclStmt:
			gd, ok := stmt.Decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				break
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if name == nil {
						continue
					}
					if vs.Type != nil {
						locals[name.Name] = idx.resolveExpr(vs.Type)
					}
					if i < len(vs.Values) {
						recordAlias(name, vs.Values[i])
					}
				}
			}
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fun.Sel == nil {
				return true
			}
			switch fun.Sel.Name {
			case "Close", "BeginClose", "Discard":
				if idx.exprHasGenerationCloseProvenance(fun.X, locals) {
					found = true
					return false
				}
			}
		case *ast.Ident:
			if closeMethods[fun.Name] {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func (idx *lifePkgIndex) exprHasGenerationCloseProvenance(expr ast.Expr, locals map[string]string) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return idx.isGenerationCloseProvenanceType(locals[t.Name])
	case *ast.ParenExpr:
		return idx.exprHasGenerationCloseProvenance(t.X, locals)
	case *ast.StarExpr:
		return idx.exprHasGenerationCloseProvenance(t.X, locals)
	default:
		return false
	}
}

func (idx *lifePkgIndex) isGenerationCloseProvenanceType(typ string) bool {
	if typ == "" {
		return false
	}
	t := strings.TrimPrefix(idx.resolve(typ, map[string]bool{}), "*")
	return isGenerationRuntimeType(t) || t == "OwnedCloser" || t == "QuiesceCloser" ||
		t == "PublishedRequestPlane"
}

func isGenerationRuntimeType(typ string) bool {
	t := strings.TrimPrefix(typ, "*")
	switch t {
	case "Generation", "GenerationRuntime", "GenerationBundle":
		return true
	default:
		return strings.HasSuffix(t, ".Generation") || strings.HasSuffix(t, ".GenerationRuntime") ||
			strings.HasSuffix(t, ".GenerationBundle")
	}
}

func embeddedName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	default:
		return lifeTypeString(expr)
	}
}

func packageNameOf(f *ast.File) string {
	if f == nil || f.Name == nil {
		return ""
	}
	return f.Name.Name
}

func isCanonicalLedgerName(name string) bool {
	return name == "ResourceLedger" || name == "ledgerEntry"
}

func isLedgerType(typ string) bool {
	t := strings.TrimPrefix(typ, "*")
	return t == "ResourceLedger" || strings.HasSuffix(t, ".ResourceLedger")
}

func isOnceType(typ string) bool {
	return typ == "sync.Once" || typ == "Once" || strings.HasSuffix(typ, ".Once")
}

func isErrorType(typ string) bool {
	return typ == "error" || strings.HasSuffix(typ, "ErrAlias") || typ == "ErrAlias"
}

func isCondType(typ string) bool {
	return typ == "*sync.Cond" || typ == "sync.Cond" || strings.HasSuffix(typ, ".Cond")
}

func isMutexType(typ string) bool {
	return typ == "sync.Mutex" || typ == "sync.RWMutex" ||
		strings.HasSuffix(typ, ".Mutex") || strings.HasSuffix(typ, ".RWMutex")
}

func isBoolOrAtomicBool(typ string) bool {
	return typ == "bool" || typ == "atomic.Bool" || strings.HasSuffix(typ, ".Bool")
}

func isDrainChanType(typ string) bool {
	return strings.HasPrefix(typ, "chan ")
}

func lifeTypeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + lifeTypeString(t.X)
	case *ast.SelectorExpr:
		return lifeTypeString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + lifeTypeString(t.Elt)
	case *ast.ChanType:
		return "chan " + lifeTypeString(t.Value)
	case *ast.MapType:
		return "map"
	case *ast.FuncType:
		return "func"
	case *ast.InterfaceType:
		return "interface"
	case *ast.StructType:
		return "struct"
	default:
		return fmt.Sprintf("%T", expr)
	}
}

func fieldNames(f *ast.Field) []string {
	if len(f.Names) == 0 {
		return nil
	}
	out := make([]string, 0, len(f.Names))
	for _, n := range f.Names {
		out = append(out, n.Name)
	}
	return out
}
