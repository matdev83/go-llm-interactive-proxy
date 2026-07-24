package archtest

import (
	"strings"
	"testing"
)

// Synthetic proofs for Task 4.4 consolidated deleted-symbol acceptance.
// Fixtures intentionally reintroduce forbidden shapes; they are in-memory only.

func TestDeletedSymbol_Detector_ReintroducesExactDeletedConcepts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		filename string
		src      string
		wantAny  []string // identity substrings; at least one must match
	}{
		{
			name:     "Built_type_decl",
			filename: "internal/infra/runtimebundle/sneak_built.go",
			src: `package runtimebundle
type Built struct{ Executor any }
`,
			wantAny: []string{"type:Built"},
		},
		{
			name:     "Built_carrier_alias",
			filename: "cmd/lipstd/sneak_hold.go",
			src: `package lipstd
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
type Hold struct{ B *runtimebundle.Built }
type BuiltAlias = runtimebundle.Built
`,
			wantAny: []string{"type:Hold", "type:BuiltAlias"},
		},
		{
			name:     "runtimebundle_Build_decl",
			filename: "internal/infra/runtimebundle/sneak_build.go",
			src: `package runtimebundle
func Build() (*int, error) { return nil, nil }
`,
			wantAny: []string{"func:Build"},
		},
		{
			name:     "runtimebundle_Build_call",
			filename: "cmd/lipstd/sneak_build_call.go",
			src: `package lipstd
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire() { _, _ = runtimebundle.Build(nil, nil, nil, nil) }
`,
			wantAny: []string{"Build"},
		},
		{
			name:     "RunWithRuntime_decl",
			filename: "internal/stdhttp/sneak_run.go",
			src: `package stdhttp
func RunWithRuntime() error { return nil }
`,
			wantAny: []string{"func:RunWithRuntime"},
		},
		{
			name:     "RunWithRuntime_call",
			filename: "cmd/lipstd/sneak_run_call.go",
			src: `package lipstd
import "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
func serve() { _ = stdhttp.RunWithRuntime(nil, nil, nil, nil, nil) }
`,
			wantAny: []string{"RunWithRuntime"},
		},
		{
			name:     "requestPlaneAsBuilt_qualified",
			filename: "internal/stdhttp/sneak_rpa.go",
			src: `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func requestPlaneAsBuilt(plane runtimebundle.RequestPlane) *runtimebundle.Built {
	return &runtimebundle.Built{}
}
func use(plane runtimebundle.RequestPlane) { _ = requestPlaneAsBuilt(plane) }
`,
			wantAny: []string{"requestPlaneAsBuilt", "func:requestPlaneAsBuilt"},
		},
		{
			name:     "requestPlaneAsBuilt_dot_import",
			filename: "internal/core/runtime/sneak_dot_rpa.go",
			src: `package runtime
import . "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
func use() { _ = requestPlaneAsBuilt(nil) }
`,
			wantAny: []string{"requestPlaneAsBuilt"},
		},
		{
			name:     "candidate_closer_aggregate",
			filename: "internal/infra/runtimebundle/sneak_closers.go",
			src: `package runtimebundle
type CandidateRuntime struct {
	Closers []func() error
}
`,
			wantAny: []string{"field:CandidateRuntime.Closers"},
		},
		{
			name:     "ledger_legacy_projection",
			filename: "internal/infra/runtimebundle/sneak_ledger.go",
			src: `package runtimebundle
type ResourceLedger struct{}
func (l *ResourceLedger) LegacyClosers() []func() error { return nil }
`,
			wantAny: []string{"LegacyClosers"},
		},
		{
			name:     "NewStandardHandler",
			filename: "internal/stdhttp/sneak_nsh.go",
			src: `package stdhttp
func NewStandardHandler() {}
`,
			wantAny: []string{"func:NewStandardHandler"},
		},
		{
			name:     "standardHTTPInputFromBuilt",
			filename: "internal/stdhttp/sneak_from_built.go",
			src: `package stdhttp
func standardHTTPInputFromBuilt() {}
`,
			wantAny: []string{"func:standardHTTPInputFromBuilt"},
		},
		{
			name:     "releaseBuiltResources",
			filename: "internal/stdhttp/sneak_release.go",
			src: `package stdhttp
func releaseBuiltResources() {}
`,
			wantAny: []string{"func:releaseBuiltResources"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := scanDeletedSymbolSource(tc.filename, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 0 {
				t.Fatalf("expected consolidated gate to reject %s, got no findings", tc.name)
			}
			joined := formatFindings(got)
			ok := false
			for _, want := range tc.wantAny {
				if findingsContainIdentity(got, want) || strings.Contains(joined, want) {
					ok = true
					break
				}
			}
			if !ok {
				t.Fatalf("expected identity containing one of %v, got:\n%s", tc.wantAny, joined)
			}
		})
	}
}

func TestDeletedSymbol_Detector_EquivalentCompatibilityDirections(t *testing.T) {
	t.Parallel()

	// Same-package unqualified Build call (Task 4.1 role-aware coverage).
	sameBuild := `package runtimebundle
func CallSame() { _, _ = Build(nil, nil, nil, nil) }
`
	got, err := scanDeletedSymbolSource("internal/infra/runtimebundle/sneak_same_build.go", sameBuild)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentityPrefix(got, "call:Build@") && !strings.Contains(formatFindings(got), "Build") {
		t.Fatalf("same-package Build call must fail consolidated gate, got %#v", got)
	}

	// Renamed generation owner closer list (Task 4.2 structural equivalent).
	renamed := `package runtimebundle
type GenerationWidget struct {
	Ledger   *ResourceLedger
	teardown []func() error
}
type ResourceLedger struct{}
`
	got, err = scanDeletedSymbolSource("internal/infra/runtimebundle/sneak_widget.go", renamed)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "field:GenerationWidget.teardown") {
		t.Fatalf("renamed closer-list owner must fail consolidated gate, got %#v", got)
	}

	// Built dependent in stdhttp (Task 3.5 / 4.3 equivalent direction).
	builtDep := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func NewExtraHandlerAdapter(b *runtimebundle.Built) {}
`
	got, err = scanDeletedSymbolSource("internal/stdhttp/sneak_built_dep.go", builtDep)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:NewExtraHandlerAdapter") {
		t.Fatalf("stdhttp Built dependency must fail consolidated gate, got %#v", got)
	}
}

func TestDeletedSymbol_Detector_Phase4AllowlistEntryRejected(t *testing.T) {
	t.Parallel()
	entries := []convergenceAllowlistEntry{
		{
			Gate: gateHostPath, Path: "cmd/lipstd/command.go",
			Identity:       "call:runServeCommand->runtimebundle.BuildBootstrap#1",
			Classification: classCall, RetirementTask: "5.2", Rationale: "phase5 ok",
		},
		{
			Gate: gateStdhttpBuilt, Path: "internal/stdhttp/handler.go",
			Identity: "func:NewStandardHandler", Classification: classDeclaration,
			RetirementTask: "4.2", Rationale: "stale phase4 grandfather",
		},
		{
			Gate: gateRuntimeConvergence, Path: "internal/stdhttp/request_plane.go",
			Identity: "func:requestPlaneAsBuilt", Classification: classAdapter,
			RetirementTask: "5.9", Rationale: "deleted symbol cannot be re-grandfathered",
		},
		{
			Gate: gateHostPath, Path: "cmd/lipstd/command.go",
			Identity: "call:sneak->stdhttp.RunWithRuntime#1", Classification: classCall,
			RetirementTask: "5.2", Rationale: "identity names deleted symbol",
		},
		{
			Gate: gateConfigLoad, Path: "cmd/lipstd/command.go",
			Identity:       "call:validateServeMultiUserGate->LoadBootstrapEffective#1",
			Classification: classCall, RetirementTask: "4.4", Rationale: "exact floor must fail",
		},
	}
	// After Task 5.5, host_path and config_load are permanently zero-tolerance:
	// every entry above is rejected (the first and last were the only Phase 5
	// exceptions that used to pass before the dual bootstrap/host-attachment
	// path and config_load wrapper owner were deleted).
	bad := validateAllowlistTask44Retirement(entries)
	if len(bad) != len(entries) {
		t.Fatalf("expected every entry rejected post-5.5 (zero Phase 5 exceptions), got %d/%d: %v", len(bad), len(entries), bad)
	}
	joined := strings.Join(bad, "\n")
	for _, want := range []string{
		"stdhttp_built",
		"runtime_convergence",
		"RunWithRuntime",
		"host_path",
		"config_load",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected rejection mentioning %q, got:\n%s", want, joined)
		}
	}
	// The former Phase 5 host_path entry is now rejected on its own: no
	// migration exception may survive past Task 5.5.
	hostPathOnly := validateAllowlistTask44Retirement(entries[:1])
	if len(hostPathOnly) == 0 {
		t.Fatal("former Phase 5 host_path entry must now be rejected (zero-exception gate)")
	}
}

func TestDeletedSymbol_Detector_MalformedRetirementTaskFailsClosed(t *testing.T) {
	t.Parallel()
	// Authentic RED: non-empty but non-X.Y retirement_task values used to bypass
	// retirementTaskAtMost (parse failure → false) and slip past the Phase 4 floor
	// when gate/identity were otherwise allowlisted. This fixture intentionally
	// uses a gate name outside permanentlyZeroToleranceAllowlistGates (every
	// named phase gate, including host_path/config_load since Task 5.5, is
	// zero-tolerance) so the malformed-retirement-task check is isolated from
	// the zero-tolerance-gate check that now runs first.
	const futureExceptionGate = "future_phase_exception_probe"
	futureOK := convergenceAllowlistEntry{
		Gate: futureExceptionGate, Path: "cmd/lipstd/command.go",
		Identity:       "call:futureCaller->future.Symbol#1",
		Classification: classCall, RetirementTask: "9.1", Rationale: "future-phase exception ok",
	}
	cases := []struct {
		name   string
		task   string
		wantIn string
	}{
		{name: "phase4_label", task: "phase4", wantIn: "malformed"},
		{name: "major_only", task: "4", wantIn: "malformed"},
		{name: "non_numeric_minor", task: "5.x", wantIn: "malformed"},
		{name: "extra_segment", task: "5.2.1", wantIn: "malformed"},
		{name: "negative_major", task: "-1.0", wantIn: "malformed"},
		{name: "phase4_floor", task: "4.4", wantIn: "Phase 4 or earlier"},
		{name: "phase4_earlier", task: "4.0", wantIn: "Phase 4 or earlier"},
		{name: "phase3", task: "3.9", wantIn: "Phase 4 or earlier"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := futureOK
			e.RetirementTask = tc.task
			e.Identity = "call:sneakMalformed->" + tc.name + "#1"
			reason := allowlistEntryViolatesTask44Retirement(e)
			if reason == "" {
				t.Fatalf("retirement_task %q must fail closed; got empty reason (bypass)", tc.task)
			}
			if !strings.Contains(reason, tc.wantIn) {
				t.Fatalf("retirement_task %q reason %q must mention %q", tc.task, reason, tc.wantIn)
			}
			if !strings.Contains(reason, tc.task) {
				t.Fatalf("reason must quote the raw retirement_task %q, got %q", tc.task, reason)
			}
		})
	}
	t.Run("valid_future_exception_passes", func(t *testing.T) {
		t.Parallel()
		if reason := allowlistEntryViolatesTask44Retirement(futureOK); reason != "" {
			t.Fatalf("well-formed non-zero-tolerance entry must pass subject to other checks, got %q", reason)
		}
	})
	t.Run("host_path_always_rejected_post_5_5", func(t *testing.T) {
		t.Parallel()
		e := convergenceAllowlistEntry{
			Gate: gateHostPath, Path: "cmd/lipstd/command.go",
			Identity:       "call:runServeCommand->runtimebundle.BuildBootstrap#1",
			Classification: classCall, RetirementTask: "5.2", Rationale: "no longer permitted",
		}
		reason := allowlistEntryViolatesTask44Retirement(e)
		if reason == "" || !strings.Contains(reason, "host_path") {
			t.Fatalf("host_path must be permanently zero-tolerance after Task 5.5, got %q", reason)
		}
	})
	t.Run("config_load_always_rejected_post_5_5", func(t *testing.T) {
		t.Parallel()
		e := convergenceAllowlistEntry{
			Gate: gateConfigLoad, Path: "cmd/lipstd/command.go",
			Identity:       "call:validateServeMultiUserGate->LoadBootstrapEffective#1",
			Classification: classCall, RetirementTask: "5.5", Rationale: "no longer permitted",
		}
		reason := allowlistEntryViolatesTask44Retirement(e)
		if reason == "" || !strings.Contains(reason, "config_load") {
			t.Fatalf("config_load must be permanently zero-tolerance after Task 5.5, got %q", reason)
		}
	})
	t.Run("does_not_normalize_malformed", func(t *testing.T) {
		t.Parallel()
		e := futureOK
		e.RetirementTask = "phase4"
		e.Identity = "call:sneakNoNormalize#1"
		reason := allowlistEntryViolatesTask44Retirement(e)
		if strings.Contains(strings.ToLower(reason), "normalized") {
			t.Fatalf("must not silently normalize malformed values, got %q", reason)
		}
		if !strings.Contains(reason, "phase4") {
			t.Fatalf("must report the raw malformed value, got %q", reason)
		}
	})
}

func TestDeletedSymbol_Detector_MountNewStandardHandlerIsStrict(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func NewStandardHandler(built *runtimebundle.Built) {}
`
	got, err := scanMountContractSource("internal/stdhttp/policy_nsh.go", src)
	if err != nil {
		t.Fatal(err)
	}
	strict := collectStrictTask32Findings(got.Findings, task32StrictFailureKinds)
	if len(strict) == 0 || !strings.Contains(strings.Join(strict, "\n"), "NewStandardHandler") {
		t.Fatalf("Task 4.4 RED: NewStandardHandler Built must be a strict mount failure after transitional exclusions retire, got %v (all=%#v)",
			strict, got.Findings)
	}
	if len(mountHelpersTransitionalAdapters) != 0 {
		t.Fatalf("Task 4.4 RED: transitional adapter map must be empty, got %v", mountHelpersTransitionalAdapters)
	}
}
