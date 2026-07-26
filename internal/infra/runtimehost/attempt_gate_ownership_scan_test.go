package runtimehost

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"strings"
	"testing"
)

// Canonical gate transition caller graph (production runtimehost).
const (
	canonicalReleaseHelper = "releaseActiveIdleLocked"
)

// TestGateOwnershipScanner_SyntheticEvasions locks practical positive evasions
// and unrelated negative controls against the ownership analyzer (not name lists).
func TestGateOwnershipScanner_SyntheticEvasions(t *testing.T) {
	t.Parallel()

	t.Run("rejects_renamed_reloadGate_owner", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_gate.go": `
package runtimehost
import ("context"; "sync")
type attemptGate struct {
	mu sync.Mutex
	shutdown bool
	pendingHUP bool
	coalesced int64
	active *attemptLease
	idleNotify chan struct{}
}
type attemptLease struct{}
func newAttemptGate() *attemptGate { return &attemptGate{} }
`,
			"owner.go": `
package runtimehost
import ("sync")
type reloadGate struct {
	mu sync.Mutex
	active *attemptLease
	idleNotify chan struct{}
	pendingHUP bool
	coalesced int64
	shutdown bool
}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { gate *attemptGate }
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
`,
		})
		got := analyzeGateOwnership(files)
		if !violationContains(got, "reloadGate") {
			t.Fatalf("expected renamed reloadGate role-shape rejection; got %v", got)
		}
	})

	t.Run("rejects_shadow_without_semantic_field_names", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_gate.go": `
package runtimehost
import ("sync")
type attemptGate struct { mu sync.Mutex }
type attemptLease struct{}
func newAttemptGate() *attemptGate { return &attemptGate{} }
`,
			"shadow.go": `
package runtimehost
import ("sync")
type shadow struct {
	lock sync.Mutex
	running *attemptLease
	wake chan struct{}
	queued bool
	stopping bool
	extras int64
}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { gate *attemptGate }
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
`,
		})
		got := analyzeGateOwnership(files)
		if !violationContains(got, "shadow") {
			t.Fatalf("expected shadow role-shape rejection; got %v", got)
		}
	})

	t.Run("rejects_aliased_legacy_bag", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_gate.go": `
package runtimehost
import ("sync")
type attemptGate struct { mu sync.Mutex }
func newAttemptGate() *attemptGate { return &attemptGate{} }
`,
			"legacy.go": `
package runtimehost
import ("context"; "sync")
type lockAlias = sync.Mutex
type cancelAlias = context.CancelFunc
type signalAlias = chan struct{}
type flagAlias = bool
type countAlias = int64
type legacyBag struct {
	lock lockAlias
	cancel cancelAlias
	done signalAlias
	a flagAlias
	b flagAlias
	n countAlias
}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { gate *attemptGate }
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
`,
		})
		got := analyzeGateOwnership(files)
		if !violationContains(got, "legacyBag") {
			t.Fatalf("expected aliased legacyBag rejection; got %v", got)
		}
	})

	t.Run("rejects_coordinator_mirrored_renamed_bag", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_gate.go": `
package runtimehost
import ("sync")
type attemptGate struct { mu sync.Mutex }
type attemptLease struct{}
func newAttemptGate() *attemptGate { return &attemptGate{} }
`,
			"coordinator.go": `
package runtimehost
import ("context"; "sync")
type Coordinator struct {
	gate *attemptGate
	mu sync.Mutex
	runningCancel context.CancelFunc
	wake chan struct{}
	queued bool
	stopping bool
	extras int64
}
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
`,
		})
		got := analyzeGateOwnership(files)
		if !violationContains(got, "Coordinator") {
			t.Fatalf("expected Coordinator mirrored bag rejection; got %v", got)
		}
	})

	t.Run("rejects_type_alias_and_defined_attemptGate", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_gate.go": `
package runtimehost
import ("sync")
type attemptGate struct { mu sync.Mutex }
func newAttemptGate() *attemptGate { return &attemptGate{} }
`,
			"alias.go": `
package runtimehost
type other = attemptGate
type otherDefined attemptGate
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { gate *attemptGate }
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
`,
		})
		got := analyzeGateOwnership(files)
		if !violationContains(got, "other") || !violationContains(got, "otherDefined") {
			t.Fatalf("expected attemptGate alias/defined rejections; got %v", got)
		}
	})

	t.Run("rejects_wrapper_struct_storing_attemptGate", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_gate.go": `
package runtimehost
type attemptGate struct{}
func newAttemptGate() *attemptGate { return &attemptGate{} }
`,
			"wrap.go": `
package runtimehost
type gateBag struct { gate *attemptGate }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { gate *attemptGate }
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
`,
		})
		got := analyzeGateOwnership(files)
		if !violationContains(got, "gateBag") {
			t.Fatalf("expected wrapper gateBag rejection; got %v", got)
		}
	})

	t.Run("rejects_newAttemptGate_value_alias_and_call", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_gate.go": `
package runtimehost
type attemptGate struct{}
func newAttemptGate() *attemptGate { return &attemptGate{} }
`,
			"alias_ctor.go": `
package runtimehost
var makeGate = newAttemptGate
func extra() *attemptGate { return makeGate() }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { gate *attemptGate }
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
`,
		})
		got := analyzeGateOwnership(files)
		if !violationContains(got, "makeGate") && !violationContains(got, "newAttemptGate") {
			t.Fatalf("expected constructor alias/extra call rejection; got %v", got)
		}
	})

	t.Run("rejects_direct_extra_newAttemptGate_caller", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_gate.go": `
package runtimehost
type attemptGate struct{}
func newAttemptGate() *attemptGate { return &attemptGate{} }
`,
			"extra.go": `
package runtimehost
func spawnGate() *attemptGate { return newAttemptGate() }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { gate *attemptGate }
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
`,
		})
		got := analyzeGateOwnership(files)
		if !violationContains(got, "spawnGate") && !violationContains(got, "extra.go") {
			t.Fatalf("expected extra newAttemptGate caller rejection; got %v", got)
		}
	})

	t.Run("rejects_extra_direct_gate_method_caller", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_gate.go": `
package runtimehost
type attemptGate struct{}
func newAttemptGate() *attemptGate { return &attemptGate{} }
func (g *attemptGate) TryStart() {}
func (g *attemptGate) WaitForIdle() {}
func (g *attemptGate) BeginShutdown() {}
func (g *attemptGate) Snapshot() {}
func (l *attemptLease) Complete() {}
func (l *attemptLease) Abandon() {}
type attemptLease struct{}
func (g *attemptGate) releaseActiveIdleLocked(l *attemptLease) {}
`,
			"extra.go": `
package runtimehost
func (c *Coordinator) helper() { c.gate.TryStart() }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { gate *attemptGate }
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
func (c *Coordinator) Reload() {
	admission := c.gate.TryStart()
	lease := admission.Lease
	defer func() { lease.Abandon() }()
	fin := lease.Complete()
	_ = fin.FollowUpLease
}
func (c *Coordinator) WaitForIdle() { c.gate.WaitForIdle() }
func (c *Coordinator) BeginShutdown() { c.gate.BeginShutdown() }
func (c *Coordinator) Status() { c.gate.Snapshot() }
`,
		})
		got := analyzeGateOwnership(files)
		got = append(got, analyzeGateCallerGraph(files)...)
		if !violationContains(got, "TryStart") {
			t.Fatalf("expected extra TryStart caller rejection; got %v", got)
		}
	})

	t.Run("rejects_method_value_alias_of_gate_transition", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_gate.go": `
package runtimehost
type attemptGate struct{}
func newAttemptGate() *attemptGate { return &attemptGate{} }
func (g *attemptGate) TryStart() {}
func (g *attemptGate) WaitForIdle() {}
func (g *attemptGate) BeginShutdown() {}
func (g *attemptGate) Snapshot() {}
func (l *attemptLease) Complete() {}
func (l *attemptLease) Abandon() {}
type attemptLease struct{ Lease *attemptLease; FollowUpLease *attemptLease }
func (g *attemptGate) releaseActiveIdleLocked(l *attemptLease) {}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { gate *attemptGate }
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
func (c *Coordinator) Reload() {
	start := c.gate.TryStart
	admission := start()
	lease := admission.Lease
	defer func() { lease.Abandon() }()
	fin := lease.Complete()
	_ = fin
}
func (c *Coordinator) WaitForIdle() { c.gate.WaitForIdle() }
func (c *Coordinator) BeginShutdown() { c.gate.BeginShutdown() }
func (c *Coordinator) Status() { c.gate.Snapshot() }
`,
		})
		got := analyzeGateCallerGraph(files)
		if !violationContains(got, "method-value") && !violationContains(got, "TryStart") {
			t.Fatalf("expected method-value alias rejection; got %v", got)
		}
	})

	t.Run("rejects_bogus_Complete_and_Abandon_despite_canonical_TryStart", func(t *testing.T) {
		t.Parallel()
		src := `
package runtimehost
type admission struct{ Lease *attemptLease }
type attemptLease struct{}
func (c *Coordinator) Reload() {
	admission := c.gate.TryStart()
	lease := admission.Lease
	defer func() { bogus.Abandon() }()
	_ = lease
	bogus.Complete()
}
`
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "coordinator.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		reload := findMethod(file, "Coordinator", "Reload")
		if reload == nil || reload.Body == nil {
			t.Fatal("Reload missing")
		}
		flow := analyzeReloadLeaseFlow(reload.Body)
		if flow.ok {
			t.Fatal("bogus Complete/Abandon must fail exact lease flow")
		}
		if !strings.Contains(strings.Join(flow.violations, "\n"), "Complete") &&
			!strings.Contains(strings.Join(flow.violations, "\n"), "Abandon") {
			t.Fatalf("expected Complete/Abandon flow violations; got %v", flow.violations)
		}
	})

	t.Run("accepts_canonical_lease_flow_with_follow_up", func(t *testing.T) {
		t.Parallel()
		src := `
package runtimehost
type admission struct{ Lease *attemptLease }
type finish struct{ FollowUpLease *attemptLease }
type attemptLease struct{}
func (c *Coordinator) Reload() {
	admission := c.gate.TryStart()
	lease := admission.Lease
	defer func() { lease.Abandon() }()
	c.runAttempt()
	fin := lease.Complete()
	lease = fin.FollowUpLease
	fin2 := lease.Complete()
	_ = fin2
}
`
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "coordinator.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		reload := findMethod(file, "Coordinator", "Reload")
		if reload == nil || reload.Body == nil {
			t.Fatal("Reload missing")
		}
		flow := analyzeReloadLeaseFlow(reload.Body)
		if !flow.ok {
			t.Fatalf("canonical follow-up lease flow must pass; got %v", flow.violations)
		}
	})

	t.Run("unrelated_same_name_methods_do_not_count", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(canonicalSyntheticCallerGraphSources(), map[string]string{
			"decoy.go": `
package runtimehost
type decoy struct{}
func (d *decoy) TryStart() {}
func (d *decoy) Complete() {}
func (d *decoy) Abandon() {}
func (d *decoy) WaitForIdle() {}
func (d *decoy) BeginShutdown() {}
func (d *decoy) Snapshot() {}
func useDecoy(d *decoy) {
	d.TryStart()
	d.Complete()
	d.Abandon()
	d.WaitForIdle()
	d.BeginShutdown()
	d.Snapshot()
}
`,
		}))
		got := analyzeGateCallerGraph(files)
		if len(got) > 0 {
			t.Fatalf("unrelated same-name methods must not count; got %v", got)
		}
	})

	t.Run("unrelated_gate_field_name_does_not_count", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(canonicalSyntheticCallerGraphSources(), map[string]string{
			"decoy_gate_field.go": `
package runtimehost
type otherGate struct{}
func (o *otherGate) TryStart() {}
func (o *otherGate) WaitForIdle() {}
func (o *otherGate) BeginShutdown() {}
func (o *otherGate) Snapshot() {}
type wrapper struct { gate *otherGate }
func (w *wrapper) sneak() {
	w.gate.TryStart()
	w.gate.WaitForIdle()
	w.gate.BeginShutdown()
	w.gate.Snapshot()
}
`,
		}))
		got := analyzeGateCallerGraph(files)
		if len(got) > 0 {
			t.Fatalf("unrelated struct field named gate must not count; got %v", got)
		}
	})

	t.Run("rejects_typed_param_gate_TryStart", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(canonicalSyntheticCallerGraphSources(), map[string]string{
			"extra.go": `
package runtimehost
import "context"
func extra(g *attemptGate) {
	g.TryStart(context.Background(), trigger{})
}
type trigger struct{}
`,
		}))
		got := analyzeGateCallerGraph(files)
		if !violationContains(got, "extra") || !violationContains(got, "TryStart") {
			t.Fatalf("expected typed *attemptGate TryStart evasion rejection; got %v", got)
		}
		if violationContains(got, "want exactly 1") {
			t.Fatalf("typed param TryStart fixture must keep canonical sites; got %v", got)
		}
	})

	t.Run("rejects_typed_param_gate_idle_shutdown_snapshot", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(canonicalSyntheticCallerGraphSources(), map[string]string{
			"extra.go": `
package runtimehost
import "context"
func extra(g *attemptGate) {
	g.WaitForIdle(context.Background())
	g.BeginShutdown()
	g.Snapshot()
}
`,
		}))
		got := analyzeGateCallerGraph(files)
		for _, method := range []string{"WaitForIdle", "BeginShutdown", "Snapshot"} {
			if !violationContains(got, "extra") || !violationContains(got, method) {
				t.Fatalf("expected typed *attemptGate %s evasion rejection; got %v", method, got)
			}
		}
	})

	t.Run("rejects_typed_param_lease_Complete_Abandon", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(canonicalSyntheticCallerGraphSources(), map[string]string{
			"extra.go": `
package runtimehost
func extra(l *attemptLease) {
	l.Complete()
	l.Abandon()
}
`,
		}))
		got := analyzeGateCallerGraph(files)
		if !violationContains(got, "extra") || !violationContains(got, "Complete") {
			t.Fatalf("expected typed *attemptLease Complete evasion rejection; got %v", got)
		}
		if !violationContains(got, "Abandon") {
			t.Fatalf("expected typed *attemptLease Abandon evasion rejection; got %v", got)
		}
		if violationContains(got, "want exactly 1") {
			t.Fatalf("typed lease fixture must keep canonical Complete/Abandon sites; got %v", got)
		}
	})

	t.Run("rejects_local_alias_of_coordinator_gate_TryStart", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(canonicalSyntheticCallerGraphSources(), map[string]string{
			"extra.go": `
package runtimehost
func sneak(c *Coordinator) {
	g := c.gate
	g.TryStart()
}
`,
		}))
		got := analyzeGateCallerGraph(files)
		if !violationContains(got, "sneak") || !violationContains(got, "TryStart") {
			t.Fatalf("expected local c.gate alias TryStart evasion rejection; got %v", got)
		}
		if violationContains(got, "want exactly 1") {
			t.Fatalf("local gate alias fixture must keep canonical TryStart site; got %v", got)
		}
	})

	t.Run("rejects_local_chained_constructor_alias_and_call", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(canonicalSyntheticCallerGraphSources(), map[string]string{
			"extra.go": `
package runtimehost
func extra() *attemptGate {
	makeGate := newAttemptGate
	again := makeGate
	return again()
}
`,
		}))
		got := analyzeGateOwnership(files)
		if !violationContains(got, "makeGate") && !violationContains(got, "again") {
			t.Fatalf("expected local chained constructor alias rejection; got %v", got)
		}
		if !violationContains(got, "extra") {
			t.Fatalf("expected local chained constructor call rejection; got %v", got)
		}
	})

	t.Run("rejects_package_scope_chained_constructor_alias_and_call", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(canonicalSyntheticCallerGraphSources(), map[string]string{
			"alias_ctor.go": `
package runtimehost
var a = newAttemptGate
var b = a
func extra() *attemptGate { return b() }
`,
		}))
		got := analyzeGateOwnership(files)
		if !violationContains(got, "a") || !violationContains(got, "b") {
			t.Fatalf("expected package-scope chained constructor alias rejection; got %v", got)
		}
		if !violationContains(got, "extra") {
			t.Fatalf("expected package-scope chained constructor call rejection; got %v", got)
		}
	})

	t.Run("rejects_method_value_via_typed_gate_and_lease_params", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(canonicalSyntheticCallerGraphSources(), map[string]string{
			"extra.go": `
package runtimehost
func extra(g *attemptGate, l *attemptLease) {
	start := g.TryStart
	idle := g.WaitForIdle
	shut := g.BeginShutdown
	snap := g.Snapshot
	done := l.Complete
	drop := l.Abandon
	_, _, _, _, _, _ = start, idle, shut, snap, done, drop
}
`,
		}))
		got := analyzeGateCallerGraph(files)
		if !violationContains(got, "method-value") {
			t.Fatalf("expected method-value alias rejection via typed params; got %v", got)
		}
		for _, method := range []string{"TryStart", "WaitForIdle", "BeginShutdown", "Snapshot", "Complete", "Abandon"} {
			if !violationContains(got, method) {
				t.Fatalf("expected method-value alias of %s; got %v", method, got)
			}
		}
	})

	t.Run("rejects_unprotected_direct_idleNotify_close", func(t *testing.T) {
		t.Parallel()
		base := canonicalSyntheticIdleCloseSources()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(base, map[string]string{
			"attempt_gate.go": strings.TrimSpace(base["attempt_gate.go"]) + `
func (g *attemptGate) bad() { close(g.idleNotify) }
`,
		}))
		got := analyzeIdleCloseOwnership(files)
		if !violationContains(got, "close") && !violationContains(got, "idleNotify") {
			t.Fatalf("expected unprotected idleNotify close rejection; got %v", got)
		}
	})

	t.Run("rejects_aliased_idleNotify_close", func(t *testing.T) {
		t.Parallel()
		base := canonicalSyntheticIdleCloseSources()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(base, map[string]string{
			"attempt_gate.go": strings.TrimSpace(base["attempt_gate.go"]) + `
func (g *attemptGate) bad() {
	x := g.idleNotify
	close(x)
}
`,
		}))
		got := analyzeIdleCloseOwnership(files)
		if !violationContains(got, "close") && !violationContains(got, "idleNotify") {
			t.Fatalf("expected aliased idleNotify close rejection; got %v", got)
		}
	})

	t.Run("rejects_chained_alias_idleNotify_close", func(t *testing.T) {
		t.Parallel()
		base := canonicalSyntheticIdleCloseSources()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(base, map[string]string{
			"attempt_gate.go": strings.TrimSpace(base["attempt_gate.go"]) + `
func (g *attemptGate) bad() {
	x := g.idleNotify
	y := x
	close(y)
}
`,
		}))
		got := analyzeIdleCloseOwnership(files)
		if !violationContains(got, "close") && !violationContains(got, "idleNotify") {
			t.Fatalf("expected chained alias idleNotify close rejection; got %v", got)
		}
	})

	t.Run("rejects_idleNotify_argument_escape", func(t *testing.T) {
		t.Parallel()
		base := canonicalSyntheticIdleCloseSources()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(base, map[string]string{
			"attempt_gate.go": strings.TrimSpace(base["attempt_gate.go"]) + `
func closeIt(ch chan struct{}) { close(ch) }
func (g *attemptGate) badDirect() { closeIt(g.idleNotify) }
func (g *attemptGate) badAlias() {
	x := g.idleNotify
	closeIt(x)
}
`,
		}))
		got := analyzeIdleCloseOwnership(files)
		if !violationContains(got, "escape") && !violationContains(got, "argument") {
			t.Fatalf("expected idleNotify argument escape rejection; got %v", got)
		}
	})

	t.Run("rejects_idleNotify_return_and_store_escape", func(t *testing.T) {
		t.Parallel()
		base := canonicalSyntheticIdleCloseSources()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(base, map[string]string{
			"attempt_gate.go": strings.TrimSpace(base["attempt_gate.go"]) + `
type bag struct{ ch chan struct{} }
var leaked chan struct{}
func (g *attemptGate) badReturn() chan struct{} { return g.idleNotify }
func (g *attemptGate) badStore() {
	b := &bag{}
	b.ch = g.idleNotify
	leaked = g.idleNotify
}
`,
		}))
		got := analyzeIdleCloseOwnership(files)
		if !violationContains(got, "escape") && !violationContains(got, "store") && !violationContains(got, "return") {
			t.Fatalf("expected idleNotify return/store escape rejection; got %v", got)
		}
	})

	t.Run("rejects_extra_close_wrapper", func(t *testing.T) {
		t.Parallel()
		base := canonicalSyntheticIdleCloseSources()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(base, map[string]string{
			"attempt_gate.go": strings.TrimSpace(base["attempt_gate.go"]) + `
func (g *attemptGate) closeIdle() { close(g.idleNotify) }
`,
		}))
		got := analyzeIdleCloseOwnership(files)
		if !violationContains(got, "closeIdle") && !violationContains(got, "close") {
			t.Fatalf("expected extra close wrapper rejection; got %v", got)
		}
	})

	t.Run("accepts_canonical_idle_close_helper", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, canonicalSyntheticIdleCloseSources())
		got := analyzeIdleCloseOwnership(files)
		if len(got) > 0 {
			t.Fatalf("canonical idle close pattern must remain accepted; got %v", got)
		}
	})

	t.Run("accepts_unrelated_channel_and_foreign_idleNotify", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, withSyntheticExtra(canonicalSyntheticIdleCloseSources(), map[string]string{
			"other.go": `
package runtimehost
type foreignGate struct{ idleNotify chan struct{} }
func closeOther() {
	ch := make(chan struct{})
	close(ch)
}
func (f *foreignGate) closeOwn() { close(f.idleNotify) }
func (f *foreignGate) aliasOwn() {
	x := f.idleNotify
	close(x)
}
`,
		}))
		got := analyzeIdleCloseOwnership(files)
		if len(got) > 0 {
			t.Fatalf("unrelated channels and foreign idleNotify must remain accepted; got %v", got)
		}
	})

	t.Run("accepts_unrelated_mutex_state_structs", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"attempt_gate.go": `
package runtimehost
import ("sync")
type attemptGate struct {
	mu sync.Mutex
	shutdown bool
	pendingHUP bool
	coalesced int64
	active *attemptLease
	idleNotify chan struct{}
}
type attemptLease struct{}
func newAttemptGate() *attemptGate { return &attemptGate{} }
`,
			"manager.go": `
package runtimehost
import ("sync"; "sync/atomic")
type Manager struct {
	mu sync.Mutex
	active atomic.Pointer[Generation]
	retained []*Generation
	shuttingDown atomic.Bool
}
type Generation struct{}
`,
			"worker.go": `
package runtimehost
import ("sync")
type LifecycleWorker struct {
	mu sync.Mutex
	stopped bool
	queue chan struct{}
}
`,
			"single_flag_worker.go": `
package runtimehost
import ("sync")
type SingleFlagWorker struct {
	mu sync.Mutex
	done chan struct{}
	stopped bool
}
`,
			"coordinator.go": `
package runtimehost
import ("sync"; "sync/atomic")
type Coordinator struct {
	gate *attemptGate
	mu sync.Mutex
	attempts atomic.Int64
	last interface{}
	lastSuccess interface{}
	lastFailure interface{}
}
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
`,
		})
		got := analyzeGateOwnership(files)
		if len(got) > 0 {
			t.Fatalf("unrelated mutex/state structs must remain accepted; got %v", got)
		}
	})
}

// canonicalSyntheticIdleCloseSources is the accepted single canonical idle-close
// ownership shape: constructor arming, WaitForIdle local wait alias, and exactly
// one releaseActiveIdleLocked close. Evasion fixtures append one violation.
func canonicalSyntheticIdleCloseSources() map[string]string {
	return map[string]string{
		"attempt_gate.go": `
package runtimehost
type attemptGate struct {
	mu struct{ Lock, Unlock func() }
	idleNotify chan struct{}
	active *attemptLease
}
type attemptLease struct {
	gate *attemptGate
	finished bool
	cancel func()
}
func newAttemptGate() *attemptGate {
	ch := make(chan struct{})
	close(ch)
	return &attemptGate{idleNotify: ch}
}
func (g *attemptGate) TryStart() {
	notify := make(chan struct{})
	g.idleNotify = notify
}
func (g *attemptGate) WaitForIdle() {
	notify := g.idleNotify
	<-notify
}
func (g *attemptGate) releaseActiveIdleLocked(l *attemptLease) func() {
	l.finished = true
	cancel := l.cancel
	notify := g.idleNotify
	close(notify)
	return cancel
}
func (l *attemptLease) Complete() {
	g := l.gate
	g.mu.Lock()
	cancel := g.releaseActiveIdleLocked(l)
	g.mu.Unlock()
	cancel()
}
func (l *attemptLease) Abandon() {
	g := l.gate
	g.mu.Lock()
	cancel := g.releaseActiveIdleLocked(l)
	g.mu.Unlock()
	cancel()
}
`,
		"coordinator.go": `
package runtimehost
type Coordinator struct { gate *attemptGate }
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
`,
	}
}

// canonicalSyntheticCallerGraphSources is a complete exact caller graph used as
// the base for provenance evasion fixtures (add one evasion on top).
func canonicalSyntheticCallerGraphSources() map[string]string {
	return map[string]string{
		"attempt_gate.go": `
package runtimehost
type attemptGate struct{}
func newAttemptGate() *attemptGate { return &attemptGate{} }
func (g *attemptGate) TryStart() {}
func (g *attemptGate) WaitForIdle() {}
func (g *attemptGate) BeginShutdown() {}
func (g *attemptGate) Snapshot() {}
type attemptLease struct{ Lease *attemptLease; FollowUpLease *attemptLease }
func (l *attemptLease) Complete() {}
func (l *attemptLease) Abandon() {}
func (g *attemptGate) releaseActiveIdleLocked(l *attemptLease) {}
`,
		"coordinator.go": `
package runtimehost
type Coordinator struct { gate *attemptGate }
func NewCoordinator() *Coordinator { return &Coordinator{gate: newAttemptGate()} }
func (c *Coordinator) Reload() {
	admission := c.gate.TryStart()
	lease := admission.Lease
	defer func() { lease.Abandon() }()
	c.gate.BeginShutdown()
	fin := lease.Complete()
	_ = fin
}
func (c *Coordinator) WaitForIdle() { c.gate.WaitForIdle() }
func (c *Coordinator) BeginShutdown() { c.gate.BeginShutdown() }
func (c *Coordinator) Status() { c.gate.Snapshot() }
`,
	}
}

func withSyntheticExtra(base map[string]string, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	maps.Copy(out, base)
	maps.Copy(out, extra)
	return out
}

func mustParseSyntheticFiles(t *testing.T, sources map[string]string) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	out := make(map[string]*ast.File, len(sources))
	for name, src := range sources {
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse synthetic %s: %v", name, err)
		}
		out[name] = file
	}
	return out
}

func violationContains(violations []string, needle string) bool {
	for _, v := range violations {
		if strings.Contains(v, needle) {
			return true
		}
	}
	return false
}

func parseProductionRuntimehostFiles(t *testing.T, fset *token.FileSet) map[string]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse production %s: %v", name, err)
		}
		out[name] = file
	}
	if len(out) == 0 {
		t.Fatal("no production Go files scanned in runtimehost")
	}
	return out
}

// analyzeGateOwnership fails closed on role/type-wise duplicate gate ownership.
func analyzeGateOwnership(files map[string]*ast.File) []string {
	var violations []string
	aliases := collectPackageTypeAliases(files)

	var attemptGateFiles []string
	for path, file := range files {
		ts := findTypeSpec(file, "attemptGate")
		if ts == nil {
			continue
		}
		if _, ok := ts.Type.(*ast.StructType); ok && ts.Assign == 0 {
			attemptGateFiles = append(attemptGateFiles, path)
		}
	}
	if len(attemptGateFiles) != 1 || attemptGateFiles[0] != "attempt_gate.go" {
		violations = append(violations, fmt.Sprintf("want exactly one attemptGate struct declaration in attempt_gate.go; got %v", attemptGateFiles))
	}

	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Name.Name == "attemptGate" {
					continue
				}
				if pointsToAttemptGate(ts, aliases) {
					kind := "defined type"
					if ts.Assign != 0 {
						kind = "alias"
					}
					violations = append(violations, fmt.Sprintf("%s: %s %q of attemptGate/*attemptGate", path, kind, ts.Name.Name))
				}
			}
		}
	}

	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				if ts.Name.Name == "Coordinator" {
					gateFields := 0
					for _, f := range st.Fields.List {
						if !storesAttemptGate(f.Type, aliases) {
							continue
						}
						for _, n := range f.Names {
							if n.Name != "gate" {
								violations = append(violations, fmt.Sprintf("%s: Coordinator gate field must be named gate; got %q", path, n.Name))
							} else {
								gateFields++
							}
						}
						if len(f.Names) == 0 {
							violations = append(violations, fmt.Sprintf("%s: Coordinator must not embed attemptGate", path))
						}
					}
					if gateFields != 1 {
						violations = append(violations, fmt.Sprintf("%s: Coordinator must have exactly one gate *attemptGate field; got %d", path, gateFields))
					}
				} else {
					for _, f := range st.Fields.List {
						if storesAttemptGate(f.Type, aliases) {
							// The gate's own lease token may hold a back-pointer.
							if path == "attempt_gate.go" && ts.Name.Name == "attemptLease" {
								continue
							}
							name := ts.Name.Name
							if len(f.Names) > 0 {
								name = ts.Name.Name + "." + f.Names[0].Name
							}
							violations = append(violations, fmt.Sprintf("%s: non-Coordinator struct %q stores/embeds attemptGate", path, name))
						}
					}
				}
				if ts.Name.Name != "attemptGate" && isRoleEquivalentGateOwner(st, aliases) {
					violations = append(violations, fmt.Sprintf("%s: renamed equivalent gate owner struct %q", path, ts.Name.Name))
				}
			}
		}
	}

	ctorCallables := collectConstructorCallables(files)
	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if name == nil || name.Name == "newAttemptGate" || i >= len(vs.Values) {
						continue
					}
					if refersToConstructorCallable(vs.Values[i], ctorCallables) {
						violations = append(violations, fmt.Sprintf("%s: package-scope constructor alias %q of newAttemptGate", path, name.Name))
					}
				}
			}
		}
	}

	var calls []ctorCallRecord
	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fnName := ""
			if fd.Name != nil {
				fnName = fd.Name.Name
			}
			localCallables := cloneStringSet(ctorCallables)
			var walk func(body *ast.BlockStmt)
			walk = func(body *ast.BlockStmt) {
				if body == nil {
					return
				}
				for _, stmt := range body.List {
					switch s := stmt.(type) {
					case *ast.AssignStmt:
						for i, rhs := range s.Rhs {
							if refersToConstructorCallable(rhs, localCallables) {
								if i < len(s.Lhs) {
									if id, ok := s.Lhs[i].(*ast.Ident); ok && id.Name != "_" && id.Name != "newAttemptGate" {
										localCallables[id.Name] = true
										violations = append(violations, fmt.Sprintf("%s: local constructor alias %q of newAttemptGate in %s", path, id.Name, fnName))
									}
								}
							}
							inspectConstructorCalls(rhs, path, fnName, localCallables, &calls)
						}
					case *ast.DeclStmt:
						gd, ok := s.Decl.(*ast.GenDecl)
						if !ok || gd.Tok != token.VAR {
							continue
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
								if i < len(vs.Values) && refersToConstructorCallable(vs.Values[i], localCallables) {
									if name.Name != "newAttemptGate" {
										localCallables[name.Name] = true
										violations = append(violations, fmt.Sprintf("%s: local constructor alias %q of newAttemptGate in %s", path, name.Name, fnName))
									}
								}
								if i < len(vs.Values) {
									inspectConstructorCalls(vs.Values[i], path, fnName, localCallables, &calls)
								}
							}
						}
					case *ast.DeferStmt:
						if s.Call == nil {
							continue
						}
						if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
							walk(lit.Body)
							continue
						}
						inspectConstructorCalls(s.Call, path, fnName, localCallables, &calls)
					case *ast.GoStmt:
						if s.Call == nil {
							continue
						}
						if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
							walk(lit.Body)
							continue
						}
						inspectConstructorCalls(s.Call, path, fnName, localCallables, &calls)
					case *ast.IfStmt:
						if s.Init != nil {
							if as, ok := s.Init.(*ast.AssignStmt); ok {
								for i, rhs := range as.Rhs {
									if refersToConstructorCallable(rhs, localCallables) {
										if i < len(as.Lhs) {
											if id, ok := as.Lhs[i].(*ast.Ident); ok && id.Name != "_" && id.Name != "newAttemptGate" {
												localCallables[id.Name] = true
												violations = append(violations, fmt.Sprintf("%s: local constructor alias %q of newAttemptGate in %s", path, id.Name, fnName))
											}
										}
									}
									inspectConstructorCalls(rhs, path, fnName, localCallables, &calls)
								}
							}
						}
						walk(s.Body)
						if s.Else != nil {
							if b, ok := s.Else.(*ast.BlockStmt); ok {
								walk(b)
							} else if elif, ok := s.Else.(*ast.IfStmt); ok {
								walk(&ast.BlockStmt{List: []ast.Stmt{elif}})
							}
						}
					case *ast.BlockStmt:
						walk(s)
					case *ast.ForStmt:
						walk(s.Body)
					case *ast.RangeStmt:
						walk(s.Body)
					case *ast.ExprStmt:
						inspectConstructorCalls(s.X, path, fnName, localCallables, &calls)
					case *ast.ReturnStmt:
						for _, r := range s.Results {
							inspectConstructorCalls(r, path, fnName, localCallables, &calls)
						}
					default:
						ast.Inspect(s, func(n ast.Node) bool {
							call, ok := n.(*ast.CallExpr)
							if !ok {
								return true
							}
							recordConstructorCall(call, path, fnName, localCallables, &calls)
							return true
						})
					}
				}
			}
			walk(fd.Body)
		}
	}
	allowed := 0
	for _, c := range calls {
		if c.direct && c.file == "coordinator.go" && c.fn == "NewCoordinator" {
			allowed++
			continue
		}
		if c.direct {
			violations = append(violations, fmt.Sprintf("%s: extra newAttemptGate/aliased constructor call in %s", c.file, c.fn))
			continue
		}
		violations = append(violations, fmt.Sprintf("%s: aliased constructor call %q in %s", c.file, c.name, c.fn))
	}
	if allowed != 1 {
		violations = append(violations, fmt.Sprintf("want exactly one newAttemptGate call in NewCoordinator; got %d allowed-site call(s)", allowed))
	}

	return violations
}

func collectPackageTypeAliases(files map[string]*ast.File) map[string]string {
	out := map[string]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				// Skip struct/interface declarations — only record named aliases
				// and defined types that rename an underlying named/composite type.
				switch ts.Type.(type) {
				case *ast.StructType, *ast.InterfaceType:
					continue
				}
				if s := typeExprString(ts.Type); s != "" {
					out[ts.Name.Name] = s
				}
			}
		}
	}
	// Recursively flatten alias chains (scalar, selector, pointer, channel).
	for range 8 {
		changed := false
		for name, under := range out {
			resolved := resolveAliasString(under, out)
			if resolved != under {
				out[name] = resolved
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return out
}

func typeExprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		if ok && t.Sel != nil {
			return pkg.Name + "." + t.Sel.Name
		}
	case *ast.StarExpr:
		inner := typeExprString(t.X)
		if inner != "" {
			return "*" + inner
		}
	case *ast.StructType:
		if t.Fields == nil || len(t.Fields.List) == 0 {
			return "struct{}"
		}
	case *ast.ChanType:
		inner := typeExprString(t.Value)
		if inner == "" {
			return ""
		}
		switch t.Dir {
		case ast.SEND:
			return "chan<- " + inner
		case ast.RECV:
			return "<-chan " + inner
		default:
			return "chan " + inner
		}
	case *ast.IndexExpr:
		base := typeExprString(t.X)
		idx := typeExprString(t.Index)
		if base != "" && idx != "" {
			return base + "[" + idx + "]"
		}
	case *ast.IndexListExpr:
		base := typeExprString(t.X)
		if base == "" {
			return ""
		}
		parts := make([]string, 0, len(t.Indices))
		for _, idx := range t.Indices {
			s := typeExprString(idx)
			if s == "" {
				return ""
			}
			parts = append(parts, s)
		}
		return base + "[" + strings.Join(parts, ",") + "]"
	case *ast.ArrayType:
		elt := typeExprString(t.Elt)
		if elt == "" {
			return ""
		}
		if t.Len == nil {
			return "[]" + elt
		}
	case *ast.FuncType:
		// Enough to recognize context.CancelFunc shape aliases that point at named funcs.
		return ""
	}
	return ""
}

func resolveAliasString(typ string, aliases map[string]string) string {
	if typ == "" {
		return ""
	}
	if after, ok := strings.CutPrefix(typ, "*"); ok {
		inner := resolveAliasString(after, aliases)
		if inner == "" {
			return typ
		}
		if strings.HasPrefix(inner, "*") {
			return inner
		}
		return "*" + inner
	}
	for _, prefix := range []string{"chan ", "chan<- ", "<-chan "} {
		if after, ok := strings.CutPrefix(typ, prefix); ok {
			inner := resolveAliasString(after, aliases)
			return prefix + inner
		}
	}
	if under, ok := aliases[typ]; ok {
		return under
	}
	return typ
}

func resolveTypeString(expr ast.Expr, aliases map[string]string) string {
	if _, ok := expr.(*ast.StructType); ok {
		return "struct{}"
	}
	raw := typeExprString(expr)
	if raw == "" {
		return ""
	}
	return resolveAliasString(raw, aliases)
}

func pointsToAttemptGate(ts *ast.TypeSpec, aliases map[string]string) bool {
	under := resolveTypeString(ts.Type, aliases)
	return under == "attemptGate" || under == "*attemptGate"
}

func storesAttemptGate(expr ast.Expr, aliases map[string]string) bool {
	under := resolveTypeString(expr, aliases)
	return under == "attemptGate" || under == "*attemptGate"
}

// isRoleEquivalentGateOwner detects equivalent gate state by resolved type
// shape, not field spellings: mutex + active authority + completion channel +
// ≥2 bool-like cells + ≥1 integer-like counter.
func isRoleEquivalentGateOwner(st *ast.StructType, aliases map[string]string) bool {
	hasMutex := false
	hasActive := false
	hasIdle := false
	boolCells := 0
	intCells := 0
	for _, f := range st.Fields.List {
		typ := resolveTypeString(f.Type, aliases)
		n := fieldArity(f)
		switch {
		case isMutexType(typ):
			hasMutex = true
		case isActiveAuthorityType(typ):
			hasActive = true
		case isStructSignalChan(typ):
			hasIdle = true
		case isBoolLikeType(typ):
			boolCells += n
		case isIntLikeType(typ):
			intCells += n
		}
	}
	return hasMutex && hasActive && hasIdle && boolCells >= 2 && intCells >= 1
}

func fieldArity(f *ast.Field) int {
	if len(f.Names) == 0 {
		return 1
	}
	return len(f.Names)
}

func isMutexType(typ string) bool {
	switch typ {
	case "sync.Mutex", "*sync.Mutex":
		return true
	default:
		return false
	}
}

func isActiveAuthorityType(typ string) bool {
	switch typ {
	case "*attemptLease", "attemptLease", "context.CancelFunc":
		return true
	default:
		return false
	}
}

func isStructSignalChan(typ string) bool {
	switch typ {
	case "chan struct{}", "chan<- struct{}", "<-chan struct{}":
		return true
	default:
		return false
	}
}

func isBoolLikeType(typ string) bool {
	switch typ {
	case "bool", "atomic.Bool", "*atomic.Bool":
		return true
	default:
		return false
	}
}

func isIntLikeType(typ string) bool {
	switch typ {
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"atomic.Int32", "atomic.Int64", "atomic.Uint32", "atomic.Uint64", "atomic.Uintptr",
		"*atomic.Int32", "*atomic.Int64", "*atomic.Uint32", "*atomic.Uint64", "*atomic.Uintptr":
		return true
	default:
		return false
	}
}

func unwrapParen(expr ast.Expr) ast.Expr {
	for {
		p, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = p.X
	}
}

func cloneStringSet(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	maps.Copy(out, in)
	return out
}

func collectConstructorCallables(files map[string]*ast.File) map[string]bool {
	callables := map[string]bool{"newAttemptGate": true}
	for range 8 {
		changed := false
		for _, file := range files {
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if name == nil || i >= len(vs.Values) {
							continue
						}
						if refersToConstructorCallable(vs.Values[i], callables) && !callables[name.Name] {
							callables[name.Name] = true
							changed = true
						}
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	return callables
}

func refersToConstructorCallable(expr ast.Expr, callables map[string]bool) bool {
	id, ok := unwrapParen(expr).(*ast.Ident)
	return ok && callables[id.Name]
}

func inspectConstructorCalls(expr ast.Expr, path, fn string, callables map[string]bool, calls *[]ctorCallRecord) {
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		recordConstructorCall(call, path, fn, callables, calls)
		return true
	})
}

type ctorCallRecord struct {
	file   string
	fn     string
	direct bool
	name   string
}

func recordConstructorCall(call *ast.CallExpr, path, fn string, callables map[string]bool, calls *[]ctorCallRecord) {
	id, ok := unwrapParen(call.Fun).(*ast.Ident)
	if !ok || !callables[id.Name] {
		return
	}
	*calls = append(*calls, ctorCallRecord{
		file:   path,
		fn:     fn,
		direct: id.Name == "newAttemptGate",
		name:   id.Name,
	})
}

func coordinatorCallsExactGateMethod(body *ast.BlockStmt, method string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != method {
			return true
		}
		if isCoordinatorGateSelector(sel.X) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isCoordinatorGateSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "gate" {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && recv.Name == "c"
}

// isCoordinatorRunnerSelector recognizes c.runner as the post-Task-6.3
// attemptRunner transaction receiver (mirrors isCoordinatorGateSelector).
func isCoordinatorRunnerSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "runner" {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && recv.Name == "c"
}

type reloadLeaseFlowResult struct {
	ok         bool
	violations []string
}

// analyzeReloadLeaseFlow proves admission originates from c.gate.TryStart,
// current lease from admission.Lease / finish.FollowUpLease, Exact Complete on
// that lease, and deferred current lease.Abandon before post-admission work.
func analyzeReloadLeaseFlow(body *ast.BlockStmt) reloadLeaseFlowResult {
	admissionVars := map[string]bool{}
	leaseVars := map[string]bool{}
	finishVars := map[string]bool{}
	var violations []string

	hasTryStart := false
	hasTrackedComplete := false
	hasTrackedAbandonDefer := false
	abandonDeferPos := token.NoPos
	firstPostAdmissionWorkPos := token.NoPos

	markPostAdmission := func(pos token.Pos) {
		if !hasTryStart || firstPostAdmissionWorkPos != token.NoPos {
			return
		}
		// Defer install itself is not post-admission panic work.
		firstPostAdmissionWorkPos = pos
	}

	var inspectStmt func(ast.Stmt)
	inspectExprForCalls := func(n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			switch sel.Sel.Name {
			case "TryStart":
				if isCoordinatorGateSelector(sel.X) {
					hasTryStart = true
				}
			case "Complete":
				if id, ok := sel.X.(*ast.Ident); ok {
					if leaseVars[id.Name] {
						hasTrackedComplete = true
					} else {
						violations = append(violations, fmt.Sprintf("Complete on non-tracked receiver %q", id.Name))
					}
				}
			case "Abandon":
				// Handled via defer inspection.
			case "runAttempt", "WithTimeout":
				if hasTryStart {
					markPostAdmission(call.Pos())
				}
			case "Run":
				if hasTryStart && isCoordinatorRunnerSelector(sel.X) {
					markPostAdmission(call.Pos())
				}
			}
			return true
		})
	}

	trackAssign := func(as *ast.AssignStmt) {
		for i, rhs := range as.Rhs {
			if i >= len(as.Lhs) {
				continue
			}
			lhs, ok := as.Lhs[i].(*ast.Ident)
			if !ok || lhs.Name == "_" {
				continue
			}
			switch r := rhs.(type) {
			case *ast.CallExpr:
				if sel, ok := r.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil {
					if sel.Sel.Name == "TryStart" && isCoordinatorGateSelector(sel.X) {
						admissionVars[lhs.Name] = true
						hasTryStart = true
						continue
					}
					if sel.Sel.Name == "Complete" {
						if id, ok := sel.X.(*ast.Ident); ok && leaseVars[id.Name] {
							finishVars[lhs.Name] = true
							hasTrackedComplete = true
							continue
						}
						if id, ok := sel.X.(*ast.Ident); ok {
							violations = append(violations, fmt.Sprintf("Complete on non-tracked receiver %q", id.Name))
						}
					}
				}
			case *ast.SelectorExpr:
				if r.Sel == nil {
					continue
				}
				base, ok := r.X.(*ast.Ident)
				if !ok {
					continue
				}
				switch r.Sel.Name {
				case "Lease":
					if admissionVars[base.Name] {
						leaseVars[lhs.Name] = true
					}
				case "FollowUpLease":
					if finishVars[base.Name] {
						leaseVars[lhs.Name] = true
					}
				}
			}
		}
		inspectExprForCalls(as)
	}

	inspectStmt = func(stmt ast.Stmt) {
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			trackAssign(s)
		case *ast.DeferStmt:
			if s.Call == nil {
				return
			}
			funLit, ok := s.Call.Fun.(*ast.FuncLit)
			if !ok || funLit.Body == nil {
				inspectExprForCalls(s.Call)
				return
			}
			abandonOK := false
			ast.Inspect(funLit.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "Abandon" {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					violations = append(violations, "deferred Abandon receiver is not an identifier")
					return false
				}
				if !leaseVars[id.Name] {
					violations = append(violations, fmt.Sprintf("deferred Abandon on non-tracked receiver %q", id.Name))
					return false
				}
				abandonOK = true
				return false
			})
			if abandonOK {
				hasTrackedAbandonDefer = true
				if abandonDeferPos == token.NoPos {
					abandonDeferPos = s.Pos()
				}
			}
		case *ast.ExprStmt:
			inspectExprForCalls(s.X)
		case *ast.IfStmt:
			if s.Init != nil {
				inspectStmt(s.Init)
			}
			inspectExprForCalls(s.Cond)
			if s.Body != nil {
				for _, st := range s.Body.List {
					inspectStmt(st)
				}
			}
			if s.Else != nil {
				inspectStmt(s.Else)
			}
		case *ast.BlockStmt:
			for _, st := range s.List {
				inspectStmt(st)
			}
		case *ast.ForStmt:
			if s.Init != nil {
				inspectStmt(s.Init)
			}
			if s.Cond != nil {
				inspectExprForCalls(s.Cond)
			}
			if s.Post != nil {
				inspectStmt(s.Post)
			}
			if s.Body != nil {
				for _, st := range s.Body.List {
					inspectStmt(st)
				}
			}
		case *ast.RangeStmt:
			if s.Body != nil {
				for _, st := range s.Body.List {
					inspectStmt(st)
				}
			}
		case *ast.ReturnStmt:
			for _, r := range s.Results {
				inspectExprForCalls(r)
			}
		case *ast.GoStmt:
			if s.Call != nil {
				inspectExprForCalls(s.Call)
				if hasTryStart {
					markPostAdmission(s.Pos())
				}
			}
		default:
			inspectExprForCalls(s)
		}
	}

	for _, stmt := range body.List {
		inspectStmt(stmt)
	}

	if !hasTryStart {
		violations = append(violations, "Reload must assign admission from exact c.gate.TryStart")
	}
	if len(admissionVars) == 0 {
		violations = append(violations, "Reload must track admission local from c.gate.TryStart")
	}
	if len(leaseVars) == 0 {
		violations = append(violations, "Reload must track current lease from admission.Lease")
	}
	if !hasTrackedComplete {
		violations = append(violations, "Reload must call Complete on tracked current lease")
	}
	if !hasTrackedAbandonDefer {
		violations = append(violations, "Reload must defer tracked current lease.Abandon")
	}
	if hasTrackedAbandonDefer && firstPostAdmissionWorkPos != token.NoPos && abandonDeferPos > firstPostAdmissionWorkPos {
		violations = append(violations, "deferred lease.Abandon must be installed before first post-admission work")
	}
	// When firstPostAdmissionWorkPos is NoPos, abandon defer presence alone is sufficient;
	// production always has runAttempt post-admission work.

	return reloadLeaseFlowResult{ok: len(violations) == 0, violations: violations}
}

type gateCallSite struct {
	file   string
	func_  string
	recv   string // "gate" or "lease" or "method-value"
	method string
	pos    token.Pos
}

type valueProvenance int

const (
	provUnknown valueProvenance = iota
	provGate
	provLease
	provAdmission
	provFinish
	provCoordinator
)

func analyzeGateCallerGraph(files map[string]*ast.File) []string {
	var violations []string
	aliases := collectPackageTypeAliases(files)
	fields := collectStructFieldTypes(files, aliases)

	var sites []gateCallSite
	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn := funcDisplayName(fd)
			env := seedFuncProvenance(fd, aliases)
			funcSites, mvViolations := collectProvenCallerSites(path, fn, fd.Body, env, aliases, fields)
			sites = append(sites, funcSites...)
			violations = append(violations, mvViolations...)
		}
	}

	count := func(method, recv string) []gateCallSite {
		var out []gateCallSite
		for _, s := range sites {
			if s.method == method && s.recv == recv {
				out = append(out, s)
			}
		}
		return out
	}

	requireExact := func(method, recv string, want int, allow func(gateCallSite) bool, desc string) {
		got := count(method, recv)
		ok := 0
		for _, s := range got {
			if allow(s) {
				ok++
				continue
			}
			violations = append(violations, fmt.Sprintf("%s: extra %s.%s caller in %s", s.file, recv, method, s.func_))
		}
		if ok != want {
			violations = append(violations, fmt.Sprintf("want exactly %d %s (%s); got %d allowed-site call(s)", want, desc, method, ok))
		}
	}

	requireExact("TryStart", "gate", 1, func(s gateCallSite) bool {
		return s.file == "coordinator.go" && strings.HasSuffix(s.func_, "Coordinator.Reload")
	}, "(*attemptGate).TryStart in Coordinator.Reload")

	requireExact("Complete", "lease", 1, func(s gateCallSite) bool {
		return s.file == "coordinator.go" && strings.HasSuffix(s.func_, "Coordinator.Reload")
	}, "(*attemptLease).Complete in Coordinator.Reload")

	requireExact("Abandon", "lease", 1, func(s gateCallSite) bool {
		return s.file == "coordinator.go" && strings.HasSuffix(s.func_, "Coordinator.Reload")
	}, "(*attemptLease).Abandon deferred in Coordinator.Reload")

	requireExact("WaitForIdle", "gate", 1, func(s gateCallSite) bool {
		return s.file == "coordinator.go" && strings.HasSuffix(s.func_, "Coordinator.WaitForIdle")
	}, "(*attemptGate).WaitForIdle in Coordinator.WaitForIdle")

	requireExact("Snapshot", "gate", 1, func(s gateCallSite) bool {
		return s.file == "coordinator.go" && strings.HasSuffix(s.func_, "Coordinator.Status")
	}, "(*attemptGate).Snapshot in Coordinator.Status")

	// BeginShutdown: exactly Coordinator.BeginShutdown + manager-shutdown fold in Reload.
	beginSites := count("BeginShutdown", "gate")
	beginOK := 0
	for _, s := range beginSites {
		if s.file == "coordinator.go" && (strings.HasSuffix(s.func_, "Coordinator.BeginShutdown") || strings.HasSuffix(s.func_, "Coordinator.Reload")) {
			beginOK++
			continue
		}
		violations = append(violations, fmt.Sprintf("%s: extra gate.BeginShutdown caller in %s", s.file, s.func_))
	}
	if beginOK != 2 {
		violations = append(violations, fmt.Sprintf("want exactly 2 (*attemptGate).BeginShutdown sites (Coordinator.BeginShutdown + Reload manager-shutdown fold); got %d", beginOK))
	}

	return violations
}

func collectStructFieldTypes(files map[string]*ast.File, aliases map[string]string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				fields := map[string]string{}
				for _, f := range st.Fields.List {
					typ := resolveTypeString(f.Type, aliases)
					for _, n := range f.Names {
						if n != nil {
							fields[n.Name] = typ
						}
					}
				}
				out[ts.Name.Name] = fields
			}
		}
	}
	return out
}

func seedFuncProvenance(fd *ast.FuncDecl, aliases map[string]string) map[string]valueProvenance {
	env := map[string]valueProvenance{}
	addFields := func(list []*ast.Field) {
		for _, f := range list {
			if f.Type == nil {
				continue
			}
			p := provenanceFromTypeExpr(f.Type, aliases)
			if p == provUnknown {
				continue
			}
			for _, n := range f.Names {
				if n != nil {
					env[n.Name] = p
				}
			}
		}
	}
	if fd.Recv != nil {
		addFields(fd.Recv.List)
	}
	if fd.Type != nil && fd.Type.Params != nil {
		addFields(fd.Type.Params.List)
	}
	return env
}

func provenanceFromTypeExpr(expr ast.Expr, aliases map[string]string) valueProvenance {
	switch resolveTypeString(expr, aliases) {
	case "*attemptGate", "attemptGate":
		return provGate
	case "*attemptLease", "attemptLease":
		return provLease
	case "*Coordinator", "Coordinator":
		return provCoordinator
	default:
		return provUnknown
	}
}

func cloneProvenance(env map[string]valueProvenance) map[string]valueProvenance {
	out := make(map[string]valueProvenance, len(env))
	maps.Copy(out, env)
	return out
}

func exprProvenance(expr ast.Expr, env map[string]valueProvenance, fields map[string]map[string]string) valueProvenance {
	expr = unwrapParen(expr)
	switch e := expr.(type) {
	case *ast.Ident:
		return env[e.Name]
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return provUnknown
		}
		base := exprProvenance(e.X, env, fields)
		switch e.Sel.Name {
		case "gate":
			if base == provCoordinator {
				if ft := fields["Coordinator"]["gate"]; ft == "*attemptGate" || ft == "attemptGate" {
					return provGate
				}
			}
			return provUnknown
		case "Lease":
			if base == provAdmission {
				return provLease
			}
		case "FollowUpLease":
			if base == provFinish {
				return provLease
			}
		}
	case *ast.CallExpr:
		sel, ok := e.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			if id, ok := unwrapParen(e.Fun).(*ast.Ident); ok && id.Name == "newAttemptGate" {
				return provGate
			}
			return provUnknown
		}
		switch sel.Sel.Name {
		case "TryStart":
			if exprProvenance(sel.X, env, fields) == provGate {
				return provAdmission
			}
		case "Complete":
			if exprProvenance(sel.X, env, fields) == provLease {
				return provFinish
			}
		}
	}
	return provUnknown
}

func collectProvenCallerSites(
	path, fn string,
	body *ast.BlockStmt,
	env map[string]valueProvenance,
	aliases map[string]string,
	fields map[string]map[string]string,
) (sites []gateCallSite, methodValueViolations []string) {
	var walk func(body *ast.BlockStmt, env map[string]valueProvenance)
	recordMethodValue := func(sel *ast.SelectorExpr, env map[string]valueProvenance) {
		if sel == nil || sel.Sel == nil {
			return
		}
		method := sel.Sel.Name
		if !isGateTransitionMethod(method) {
			return
		}
		switch method {
		case "TryStart", "WaitForIdle", "BeginShutdown", "Snapshot":
			if exprProvenance(sel.X, env, fields) != provGate {
				return
			}
			sites = append(sites, gateCallSite{file: path, func_: fn, recv: "method-value", method: method, pos: sel.Pos()})
			methodValueViolations = append(methodValueViolations, fmt.Sprintf("%s: method-value alias of %s in %s", path, method, fn))
		case "Complete", "Abandon":
			if exprProvenance(sel.X, env, fields) != provLease {
				return
			}
			sites = append(sites, gateCallSite{file: path, func_: fn, recv: "method-value", method: method, pos: sel.Pos()})
			methodValueViolations = append(methodValueViolations, fmt.Sprintf("%s: method-value alias of lease.%s in %s", path, method, fn))
		}
	}
	recordCall := func(call *ast.CallExpr, env map[string]valueProvenance) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		method := sel.Sel.Name
		if !isGateTransitionMethod(method) {
			return
		}
		switch method {
		case "TryStart", "WaitForIdle", "BeginShutdown", "Snapshot":
			if exprProvenance(sel.X, env, fields) == provGate {
				sites = append(sites, gateCallSite{file: path, func_: fn, recv: "gate", method: method, pos: call.Pos()})
			}
		case "Complete", "Abandon":
			if exprProvenance(sel.X, env, fields) == provLease {
				sites = append(sites, gateCallSite{file: path, func_: fn, recv: "lease", method: method, pos: call.Pos()})
			}
		}
	}
	inspectExpr := func(expr ast.Expr, env map[string]valueProvenance) {
		if expr == nil {
			return
		}
		ast.Inspect(expr, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				recordCall(x, env)
				return true
			case *ast.SelectorExpr:
				// Method-value: selector that is not immediately called.
				// Detected via assignment/value-spec handlers; skip here.
				return true
			}
			return true
		})
	}
	applyAssign := func(as *ast.AssignStmt, env map[string]valueProvenance) {
		for _, rhs := range as.Rhs {
			// Method-value aliases on RHS.
			if sel, ok := unwrapParen(rhs).(*ast.SelectorExpr); ok {
				recordMethodValue(sel, env)
			}
			inspectExpr(rhs, env)
		}
		for i, rhs := range as.Rhs {
			if i >= len(as.Lhs) {
				continue
			}
			lhs, ok := as.Lhs[i].(*ast.Ident)
			if !ok || lhs.Name == "_" {
				continue
			}
			if p := exprProvenance(rhs, env, fields); p != provUnknown {
				env[lhs.Name] = p
			}
		}
	}
	applyValueSpec := func(vs *ast.ValueSpec, env map[string]valueProvenance) {
		if vs.Type != nil {
			if p := provenanceFromTypeExpr(vs.Type, aliases); p != provUnknown {
				for _, name := range vs.Names {
					if name != nil {
						env[name.Name] = p
					}
				}
			}
		}
		for i, name := range vs.Names {
			if name == nil || i >= len(vs.Values) {
				continue
			}
			if sel, ok := unwrapParen(vs.Values[i]).(*ast.SelectorExpr); ok {
				recordMethodValue(sel, env)
			}
			inspectExpr(vs.Values[i], env)
			if p := exprProvenance(vs.Values[i], env, fields); p != provUnknown {
				env[name.Name] = p
			}
		}
	}
	walk = func(body *ast.BlockStmt, env map[string]valueProvenance) {
		if body == nil {
			return
		}
		for _, stmt := range body.List {
			switch s := stmt.(type) {
			case *ast.AssignStmt:
				applyAssign(s, env)
			case *ast.DeclStmt:
				gd, ok := s.Decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						applyValueSpec(vs, env)
					}
				}
			case *ast.DeferStmt:
				if s.Call == nil {
					continue
				}
				if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
					child := cloneProvenance(env)
					if lit.Type != nil && lit.Type.Params != nil {
						for _, f := range lit.Type.Params.List {
							if f.Type == nil {
								continue
							}
							p := provenanceFromTypeExpr(f.Type, aliases)
							if p == provUnknown {
								continue
							}
							for _, n := range f.Names {
								if n != nil {
									child[n.Name] = p
								}
							}
						}
					}
					walk(lit.Body, child)
					continue
				}
				inspectExpr(s.Call, env)
			case *ast.GoStmt:
				if s.Call == nil {
					continue
				}
				if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
					walk(lit.Body, cloneProvenance(env))
					continue
				}
				inspectExpr(s.Call, env)
			case *ast.ExprStmt:
				if sel, ok := unwrapParen(s.X).(*ast.SelectorExpr); ok {
					recordMethodValue(sel, env)
				}
				inspectExpr(s.X, env)
			case *ast.IfStmt:
				child := cloneProvenance(env)
				if s.Init != nil {
					if as, ok := s.Init.(*ast.AssignStmt); ok {
						applyAssign(as, child)
					}
				}
				inspectExpr(s.Cond, child)
				walk(s.Body, child)
				if s.Else != nil {
					elseEnv := cloneProvenance(env)
					switch e := s.Else.(type) {
					case *ast.BlockStmt:
						walk(e, elseEnv)
					case *ast.IfStmt:
						walk(&ast.BlockStmt{List: []ast.Stmt{e}}, elseEnv)
					}
				}
			case *ast.BlockStmt:
				walk(s, cloneProvenance(env))
			case *ast.ForStmt:
				child := cloneProvenance(env)
				if s.Init != nil {
					if as, ok := s.Init.(*ast.AssignStmt); ok {
						applyAssign(as, child)
					}
				}
				if s.Cond != nil {
					inspectExpr(s.Cond, child)
				}
				if s.Post != nil {
					if as, ok := s.Post.(*ast.AssignStmt); ok {
						applyAssign(as, child)
					}
				}
				walk(s.Body, child)
			case *ast.RangeStmt:
				child := cloneProvenance(env)
				inspectExpr(s.X, env)
				walk(s.Body, child)
			case *ast.ReturnStmt:
				for _, r := range s.Results {
					inspectExpr(r, env)
				}
			default:
				ast.Inspect(s, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.CallExpr:
						recordCall(x, env)
					case *ast.AssignStmt:
						applyAssign(x, env)
						return false
					}
					return true
				})
			}
		}
	}
	walk(body, env)
	return sites, methodValueViolations
}

func isGateTransitionMethod(name string) bool {
	switch name {
	case "TryStart", "Complete", "Abandon", "WaitForIdle", "BeginShutdown", "Snapshot":
		return true
	default:
		return false
	}
}

func funcDisplayName(fd *ast.FuncDecl) string {
	name := ""
	if fd.Name != nil {
		name = fd.Name.Name
	}
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return name
	}
	recv := receiverTypeName(fd.Recv.List[0].Type)
	if recv == "" {
		return name
	}
	return recv + "." + name
}

// analyzeIdleCloseOwnership enforces one constructor close and one canonical
// lock-owned release helper as the only idleNotify close path, using value
// provenance (not identifier name heuristics) for gate idle channels.
func analyzeIdleCloseOwnership(files map[string]*ast.File) []string {
	var violations []string
	gateFile := files["attempt_gate.go"]
	if gateFile == nil {
		return []string{"attempt_gate.go missing for idle close analysis"}
	}
	aliases := collectPackageTypeAliases(files)

	release := findMethod(gateFile, "attemptGate", canonicalReleaseHelper)
	if release == nil || release.Body == nil {
		violations = append(violations, fmt.Sprintf("missing canonical release helper %s", canonicalReleaseHelper))
		return violations
	}

	complete := findMethod(gateFile, "attemptLease", "Complete")
	abandon := findMethod(gateFile, "attemptLease", "Abandon")
	if complete == nil || complete.Body == nil {
		violations = append(violations, "attemptLease.Complete missing")
	}
	if abandon == nil || abandon.Body == nil {
		violations = append(violations, "attemptLease.Abandon missing")
	}
	if complete != nil && complete.Body != nil {
		if !callsReleaseHelperUnderLock(complete.Body) {
			violations = append(violations, "Complete must call releaseActiveIdleLocked after Lock and before Unlock")
		}
	}
	if abandon != nil && abandon.Body != nil {
		if !callsReleaseHelperUnderLock(abandon.Body) {
			violations = append(violations, "Abandon must call releaseActiveIdleLocked after Lock and before Unlock")
		}
	}

	type closeHit struct {
		file string
		fn   string
	}
	var idleCloses []closeHit
	var ctorCloses int
	releaseCloses := 0

	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn := funcDisplayName(fd)
			scan := scanIdleNotifyInFunc(path, fd, aliases)
			violations = append(violations, scan.escapes...)

			if path == "attempt_gate.go" && fd.Name != nil && fd.Name.Name == "newAttemptGate" {
				ctorCloses += countBuiltinCloseCalls(fd.Body)
				// Proven idle closes inside the constructor are unauthorized;
				// the allowed ctor close is the fresh make() channel.
				for i := 0; i < scan.closes; i++ {
					violations = append(violations, fmt.Sprintf("%s: unauthorized idleNotify close in %s", path, fn))
				}
				continue
			}

			if path == "attempt_gate.go" && strings.HasSuffix(fn, "attemptGate."+canonicalReleaseHelper) {
				releaseCloses += scan.closes
				continue
			}

			if scan.closes == 0 {
				continue
			}
			switch {
			case strings.HasSuffix(fn, "attemptLease.Complete"):
				violations = append(violations, "Complete must not close idleNotify directly")
			case strings.HasSuffix(fn, "attemptLease.Abandon"):
				violations = append(violations, "Abandon must not close idleNotify directly")
			default:
				for i := 0; i < scan.closes; i++ {
					idleCloses = append(idleCloses, closeHit{file: path, fn: fn})
				}
			}
		}
	}

	if ctorCloses != 1 {
		violations = append(violations, fmt.Sprintf("want exactly one constructor idle close in newAttemptGate; got %d", ctorCloses))
	}
	for _, h := range idleCloses {
		violations = append(violations, fmt.Sprintf("%s: unauthorized idleNotify close in %s", h.file, h.fn))
	}
	if releaseCloses != 1 {
		violations = append(violations, fmt.Sprintf("want exactly one idleNotify close in %s; got %d", canonicalReleaseHelper, releaseCloses))
	}

	// Only Complete and Abandon may call the release helper.
	var helperCallers []string
	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Name == nil {
				continue
			}
			fn := funcDisplayName(fd)
			if strings.HasSuffix(fn, "attemptGate."+canonicalReleaseHelper) {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != canonicalReleaseHelper {
					return true
				}
				helperCallers = append(helperCallers, path+":"+fn)
				return true
			})
		}
	}
	wantCallers := map[string]bool{
		"attempt_gate.go:attemptLease.Complete": true,
		"attempt_gate.go:attemptLease.Abandon":  true,
	}
	for _, c := range helperCallers {
		if !wantCallers[c] {
			violations = append(violations, fmt.Sprintf("unauthorized %s caller %s", canonicalReleaseHelper, c))
		}
		delete(wantCallers, c)
	}
	for missing := range wantCallers {
		violations = append(violations, fmt.Sprintf("missing required %s caller %s", canonicalReleaseHelper, missing))
	}

	return violations
}

type idleNotifyScan struct {
	closes  int
	escapes []string
}

// scanIdleNotifyInFunc tracks proven attemptGate.idleNotify values through local
// aliases and reports closes plus escape via arguments, returns, or stores.
func scanIdleNotifyInFunc(path string, fd *ast.FuncDecl, aliases map[string]string) idleNotifyScan {
	var result idleNotifyScan
	fn := funcDisplayName(fd)
	gates := map[string]bool{}
	leases := map[string]bool{}
	idles := map[string]bool{}
	locals := map[string]bool{}

	seedTyped := func(list []*ast.Field) {
		for _, f := range list {
			if f.Type == nil {
				continue
			}
			typ := resolveTypeString(f.Type, aliases)
			for _, n := range f.Names {
				if n == nil {
					continue
				}
				locals[n.Name] = true
				switch typ {
				case "*attemptGate", "attemptGate":
					gates[n.Name] = true
				case "*attemptLease", "attemptLease":
					leases[n.Name] = true
				}
			}
		}
	}
	if fd.Recv != nil {
		seedTyped(fd.Recv.List)
	}
	if fd.Type != nil && fd.Type.Params != nil {
		seedTyped(fd.Type.Params.List)
	}

	var (
		exprIsProvenIdle func(expr ast.Expr, gates, leases, idles map[string]bool) bool
		exprIsProvenGate func(expr ast.Expr, gates, leases map[string]bool) bool
		walk             func(body *ast.BlockStmt, gates, leases, idles, locals map[string]bool)
	)

	exprIsProvenGate = func(expr ast.Expr, gates, leases map[string]bool) bool {
		expr = unwrapParen(expr)
		switch e := expr.(type) {
		case *ast.Ident:
			return gates[e.Name]
		case *ast.SelectorExpr:
			if e.Sel != nil && e.Sel.Name == "gate" {
				if id, ok := unwrapParen(e.X).(*ast.Ident); ok && leases[id.Name] {
					return true
				}
			}
		case *ast.CallExpr:
			if id, ok := unwrapParen(e.Fun).(*ast.Ident); ok && id.Name == "newAttemptGate" {
				return true
			}
		}
		return false
	}

	exprIsProvenIdle = func(expr ast.Expr, gates, leases, idles map[string]bool) bool {
		expr = unwrapParen(expr)
		if id, ok := expr.(*ast.Ident); ok {
			return idles[id.Name]
		}
		sel, ok := expr.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "idleNotify" {
			return false
		}
		base := unwrapParen(sel.X)
		switch b := base.(type) {
		case *ast.Ident:
			return gates[b.Name]
		case *ast.SelectorExpr:
			if b.Sel != nil && b.Sel.Name == "gate" {
				if id, ok := unwrapParen(b.X).(*ast.Ident); ok && leases[id.Name] {
					return true
				}
			}
		}
		return false
	}

	holdIdle := func(expr ast.Expr, gates, leases, idles map[string]bool) bool {
		expr = unwrapParen(expr)
		if exprIsProvenIdle(expr, gates, leases, idles) {
			return true
		}
		if u, ok := expr.(*ast.UnaryExpr); ok && u.Op == token.AND {
			expr = unwrapParen(u.X)
		}
		cl, ok := expr.(*ast.CompositeLit)
		if !ok {
			return false
		}
		for _, elt := range cl.Elts {
			switch e := elt.(type) {
			case *ast.KeyValueExpr:
				if exprIsProvenIdle(e.Value, gates, leases, idles) {
					return true
				}
			default:
				if exprIsProvenIdle(e, gates, leases, idles) {
					return true
				}
			}
		}
		return false
	}

	recordEscape := func(kind string) {
		result.escapes = append(result.escapes, fmt.Sprintf("%s: idleNotify %s escape in %s", path, kind, fn))
	}

	inspectCall := func(call *ast.CallExpr, gates, leases, idles map[string]bool) {
		if id, ok := unwrapParen(call.Fun).(*ast.Ident); ok && id.Name == "close" && len(call.Args) == 1 {
			if exprIsProvenIdle(call.Args[0], gates, leases, idles) {
				result.closes++
			}
			return
		}
		if isIdleScanBuiltin(call.Fun) {
			return
		}
		for _, arg := range call.Args {
			if exprIsProvenIdle(arg, gates, leases, idles) {
				recordEscape("argument")
				return
			}
		}
	}

	applyAssign := func(as *ast.AssignStmt, gates, leases, idles, locals map[string]bool) {
		if as.Tok == token.DEFINE {
			for _, lhs := range as.Lhs {
				if id, ok := unwrapParen(lhs).(*ast.Ident); ok && id.Name != "_" {
					locals[id.Name] = true
				}
			}
		}
		for i, rhs := range as.Rhs {
			if i >= len(as.Lhs) {
				continue
			}
			lhs := unwrapParen(as.Lhs[i])
			if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
				if exprIsProvenGate(rhs, gates, leases) {
					gates[id.Name] = true
				}
				if id2, ok := unwrapParen(rhs).(*ast.Ident); ok && leases[id2.Name] {
					leases[id.Name] = true
				}
				if exprIsProvenIdle(rhs, gates, leases, idles) {
					if locals[id.Name] {
						idles[id.Name] = true
					} else {
						recordEscape("store")
					}
				}
				continue
			}
			if exprIsProvenIdle(rhs, gates, leases, idles) || holdIdle(rhs, gates, leases, idles) {
				recordEscape("store")
			}
		}
		for _, rhs := range as.Rhs {
			ast.Inspect(rhs, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					inspectCall(call, gates, leases, idles)
				}
				return true
			})
		}
	}

	applyValueSpec := func(vs *ast.ValueSpec, gates, leases, idles, locals map[string]bool) {
		for _, name := range vs.Names {
			if name != nil {
				locals[name.Name] = true
			}
		}
		if vs.Type != nil {
			typ := resolveTypeString(vs.Type, aliases)
			for _, name := range vs.Names {
				if name == nil {
					continue
				}
				switch typ {
				case "*attemptGate", "attemptGate":
					gates[name.Name] = true
				case "*attemptLease", "attemptLease":
					leases[name.Name] = true
				}
			}
		}
		for i, name := range vs.Names {
			if name == nil || i >= len(vs.Values) {
				continue
			}
			rhs := vs.Values[i]
			if exprIsProvenGate(rhs, gates, leases) {
				gates[name.Name] = true
			}
			if id, ok := unwrapParen(rhs).(*ast.Ident); ok && leases[id.Name] {
				leases[name.Name] = true
			}
			if exprIsProvenIdle(rhs, gates, leases, idles) {
				idles[name.Name] = true
			}
			ast.Inspect(rhs, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					inspectCall(call, gates, leases, idles)
				}
				return true
			})
		}
	}

	walk = func(body *ast.BlockStmt, gates, leases, idles, locals map[string]bool) {
		if body == nil {
			return
		}
		for _, stmt := range body.List {
			switch s := stmt.(type) {
			case *ast.AssignStmt:
				applyAssign(s, gates, leases, idles, locals)
			case *ast.DeclStmt:
				gd, ok := s.Decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						applyValueSpec(vs, gates, leases, idles, locals)
					}
				}
			case *ast.ReturnStmt:
				for _, r := range s.Results {
					if exprIsProvenIdle(r, gates, leases, idles) || holdIdle(r, gates, leases, idles) {
						recordEscape("return")
					}
					ast.Inspect(r, func(n ast.Node) bool {
						if call, ok := n.(*ast.CallExpr); ok {
							inspectCall(call, gates, leases, idles)
						}
						return true
					})
				}
			case *ast.ExprStmt:
				ast.Inspect(s.X, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						inspectCall(call, gates, leases, idles)
					}
					return true
				})
			case *ast.DeferStmt:
				if s.Call == nil {
					continue
				}
				if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
					childG, childL, childI, childLoc := cloneStringSet(gates), cloneStringSet(leases), cloneStringSet(idles), cloneStringSet(locals)
					if lit.Type != nil && lit.Type.Params != nil {
						seedInto := func(list []*ast.Field) {
							for _, f := range list {
								if f.Type == nil {
									continue
								}
								typ := resolveTypeString(f.Type, aliases)
								for _, n := range f.Names {
									if n == nil {
										continue
									}
									childLoc[n.Name] = true
									switch typ {
									case "*attemptGate", "attemptGate":
										childG[n.Name] = true
									case "*attemptLease", "attemptLease":
										childL[n.Name] = true
									}
								}
							}
						}
						seedInto(lit.Type.Params.List)
					}
					walk(lit.Body, childG, childL, childI, childLoc)
					continue
				}
				inspectCall(s.Call, gates, leases, idles)
			case *ast.GoStmt:
				if s.Call == nil {
					continue
				}
				if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
					walk(lit.Body, cloneStringSet(gates), cloneStringSet(leases), cloneStringSet(idles), cloneStringSet(locals))
					continue
				}
				inspectCall(s.Call, gates, leases, idles)
			case *ast.IfStmt:
				childG, childL, childI, childLoc := cloneStringSet(gates), cloneStringSet(leases), cloneStringSet(idles), cloneStringSet(locals)
				if s.Init != nil {
					if as, ok := s.Init.(*ast.AssignStmt); ok {
						applyAssign(as, childG, childL, childI, childLoc)
					}
				}
				if s.Cond != nil {
					ast.Inspect(s.Cond, func(n ast.Node) bool {
						if call, ok := n.(*ast.CallExpr); ok {
							inspectCall(call, childG, childL, childI)
						}
						return true
					})
				}
				walk(s.Body, childG, childL, childI, childLoc)
				if s.Else != nil {
					elseG, elseL, elseI, elseLoc := cloneStringSet(gates), cloneStringSet(leases), cloneStringSet(idles), cloneStringSet(locals)
					switch e := s.Else.(type) {
					case *ast.BlockStmt:
						walk(e, elseG, elseL, elseI, elseLoc)
					case *ast.IfStmt:
						walk(&ast.BlockStmt{List: []ast.Stmt{e}}, elseG, elseL, elseI, elseLoc)
					}
				}
			case *ast.BlockStmt:
				walk(s, cloneStringSet(gates), cloneStringSet(leases), cloneStringSet(idles), cloneStringSet(locals))
			case *ast.ForStmt:
				childG, childL, childI, childLoc := cloneStringSet(gates), cloneStringSet(leases), cloneStringSet(idles), cloneStringSet(locals)
				if s.Init != nil {
					if as, ok := s.Init.(*ast.AssignStmt); ok {
						applyAssign(as, childG, childL, childI, childLoc)
					}
				}
				if s.Cond != nil {
					ast.Inspect(s.Cond, func(n ast.Node) bool {
						if call, ok := n.(*ast.CallExpr); ok {
							inspectCall(call, childG, childL, childI)
						}
						return true
					})
				}
				if s.Post != nil {
					if as, ok := s.Post.(*ast.AssignStmt); ok {
						applyAssign(as, childG, childL, childI, childLoc)
					}
				}
				walk(s.Body, childG, childL, childI, childLoc)
			case *ast.RangeStmt:
				childG, childL, childI, childLoc := cloneStringSet(gates), cloneStringSet(leases), cloneStringSet(idles), cloneStringSet(locals)
				walk(s.Body, childG, childL, childI, childLoc)
			case *ast.SelectStmt:
				for _, clause := range s.Body.List {
					cc, ok := clause.(*ast.CommClause)
					if !ok {
						continue
					}
					childG, childL, childI, childLoc := cloneStringSet(gates), cloneStringSet(leases), cloneStringSet(idles), cloneStringSet(locals)
					if cc.Comm != nil {
						switch c := cc.Comm.(type) {
						case *ast.AssignStmt:
							applyAssign(c, childG, childL, childI, childLoc)
						case *ast.ExprStmt:
							ast.Inspect(c.X, func(n ast.Node) bool {
								if call, ok := n.(*ast.CallExpr); ok {
									inspectCall(call, childG, childL, childI)
								}
								return true
							})
						}
					}
					walk(&ast.BlockStmt{List: cc.Body}, childG, childL, childI, childLoc)
				}
			case *ast.SendStmt:
				if exprIsProvenIdle(s.Value, gates, leases, idles) {
					recordEscape("store")
				}
			default:
				ast.Inspect(s, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.CallExpr:
						inspectCall(x, gates, leases, idles)
					case *ast.AssignStmt:
						applyAssign(x, gates, leases, idles, locals)
						return false
					case *ast.ReturnStmt:
						for _, r := range x.Results {
							if exprIsProvenIdle(r, gates, leases, idles) || holdIdle(r, gates, leases, idles) {
								recordEscape("return")
							}
						}
					}
					return true
				})
			}
		}
	}

	walk(fd.Body, gates, leases, idles, locals)
	return result
}

func isIdleScanBuiltin(fun ast.Expr) bool {
	id, ok := unwrapParen(fun).(*ast.Ident)
	if !ok {
		return false
	}
	switch id.Name {
	case "close", "len", "cap", "make", "new", "append", "copy", "delete",
		"panic", "recover", "print", "println", "complex", "real", "imag",
		"min", "max", "clear":
		return true
	default:
		return false
	}
}

func countBuiltinCloseCalls(body *ast.BlockStmt) int {
	n := 0
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if ok && id.Name == "close" && len(call.Args) == 1 {
			n++
		}
		return true
	})
	return n
}

func callsReleaseHelperUnderLock(body *ast.BlockStmt) bool {
	lockPos := token.NoPos
	helperPos := token.NoPos
	unlockAfterHelper := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return true
		}
		switch sel.Sel.Name {
		case "Lock":
			if lockPos == token.NoPos {
				lockPos = call.Pos()
			}
		case canonicalReleaseHelper:
			if helperPos == token.NoPos {
				helperPos = call.Pos()
			}
		case "Unlock":
			if helperPos != token.NoPos && call.Pos() > helperPos {
				unlockAfterHelper = true
			}
		}
		return true
	})
	return lockPos != token.NoPos && helperPos != token.NoPos && lockPos < helperPos && unlockAfterHelper
}

// reloadAbandonDeferBeforeRunAttempt requires defer-before-runAttempt ordering.
func reloadAbandonDeferBeforeRunAttempt(body *ast.BlockStmt) bool {
	flow := analyzeReloadLeaseFlow(body)
	if !flow.ok {
		return false
	}
	abandonPos := token.NoPos
	runPos := token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.DeferStmt:
			if n.Call == nil || abandonPos != token.NoPos {
				return true
			}
			funLit, ok := n.Call.Fun.(*ast.FuncLit)
			if !ok || funLit.Body == nil {
				return true
			}
			hasAbandon := false
			ast.Inspect(funLit.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel != nil && sel.Sel.Name == "Abandon" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "lease" {
						hasAbandon = true
						return false
					}
				}
				return true
			})
			if hasAbandon {
				abandonPos = n.Pos()
			}
		case *ast.CallExpr:
			if runPos != token.NoPos {
				return true
			}
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			if sel.Sel.Name == "runAttempt" {
				runPos = n.Pos()
				return true
			}
			if sel.Sel.Name == "Run" && isCoordinatorRunnerSelector(sel.X) {
				runPos = n.Pos()
			}
		}
		return true
	})
	return abandonPos != token.NoPos && runPos != token.NoPos && abandonPos < runPos
}
