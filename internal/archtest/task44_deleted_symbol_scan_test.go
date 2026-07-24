package archtest

import (
	"fmt"
	"strconv"
	"strings"
)

// Task 4.4 consolidates permanent deleted-symbol acceptance for Phase 4
// retirement (req 3.1-3.10, 11.2-11.9). Scanners are reused from Tasks 1.4 /
// 3.5 / 4.1 / 4.2 / 4.3; this file only composes them and enforces allowlist
// retirement floors.

const (
	gateDeletedSymbol = "deleted_symbol"
	// task44AllowlistRetirementFloor is the last Phase 4 task. Allowlist entries
	// with retirement_task at or below this value are forbidden; Phase 5+
	// startup/host-path exceptions remain until their named tasks retire them.
	task44AllowlistRetirementFloor = "4.4"
)

// deletedSymbolScanner binds a named role-aware scanner used by the
// consolidated deleted-symbol acceptance gate.
type deletedSymbolScanner struct {
	Name string
	Gate string
	Scan convergenceScanner
}

// deletedSymbolScanners is the Task 4.4 composition order. Exact names and
// equivalent compatibility directions are covered by role-aware scanners from
// prior tasks; this list must stay the single acceptance inventory.
var deletedSymbolScanners = []deletedSymbolScanner{
	{Name: "runtime_convergence", Gate: gateRuntimeConvergence, Scan: scanRuntimeConvergenceSource},
	{Name: "compat_http_symbols", Gate: gateCompatHTTPSymbols, Scan: scanCompatHTTPSymbolsSource},
	{Name: "stdhttp_built", Gate: gateStdhttpBuilt, Scan: scanStdhttpBuiltSource},
	{Name: "candidate_legacy_closers", Gate: gateCandidateLegacyClosers, Scan: scanCandidateLegacyClosersSource},
	{Name: "task41_build_call", Gate: gateTask41BuildCall, Scan: scanTask41BuildCallSource},
	{Name: "task41_built_carrier", Gate: gateTask41BuiltCarrier, Scan: scanTask41BuiltCarrierSource},
	{Name: "task42_built_type_decl", Gate: gateTask42BuiltTypeDecl, Scan: scanTask42BuiltTypeDeclSource},
	{Name: "task42_build_decl", Gate: gateTask42BuildDecl, Scan: scanTask42BuildDeclSource},
	{Name: "task42_candidate_closer_field", Gate: gateTask42CandidateCloserFld, Scan: scanTask42CandidateCloserFieldSource},
	{Name: "task42_ledger_closer_projection", Gate: gateTask42LedgerCloserProjection, Scan: scanTask42LedgerCloserProjectionSource},
	{Name: "task43_deleted_serve", Gate: gateTask43DeletedServe, Scan: scanTask43DeletedServeSource},
	{Name: "task55_deleted_bootstrap", Gate: gateTask55DeletedBootstrap, Scan: scanTask55DeletedBootstrapSource},
}

// permanentlyZeroToleranceAllowlistGates may never appear in the runtime-
// convergence allowlist. Task 5.5 retires the last scheduled Phase 5
// exceptions (host_path, config_load): the dual bootstrap/host-attachment
// path and the wrapper startup-load owner are deleted, so these gates join
// the permanent zero-exception set alongside every earlier phase gate.
var permanentlyZeroToleranceAllowlistGates = map[string]bool{
	gateRuntimeConvergence:           true,
	gateReloadContract:               true,
	gateBroadRequestPlane:            true,
	gateCompatHTTPSymbols:            true,
	gateFocusedHTTPLifecycle:         true,
	gateStdhttpBuilt:                 true,
	gateCanonicalClosers:             true,
	gateCandidateLegacyClosers:       true,
	gateComposeInventory:             true,
	gateTask41BuildCall:              true,
	gateTask41BuiltCarrier:           true,
	gateTask41TestLegacyCaller:       true,
	gateTask41ReplacementAggregate:   true,
	gateTask42BuiltTypeDecl:          true,
	gateTask42BuildDecl:              true,
	gateTask42CandidateCloserFld:     true,
	gateTask42LedgerCloserProjection: true,
	gateTask42TestCtorInProd:         true,
	gateTask43SoleServeAPI:           true,
	gateTask43DeletedServe:           true,
	gateTask43AppOwnedServe:          true,
	gateTask43StaleTestNames:         true,
	gateDeletedSymbol:                true,
	gateHostPath:                     true,
	gateConfigLoad:                   true,
	gateTask55DeletedBootstrap:       true,
}

// deletedPhase4AllowlistIdentityTokens are identity substrings that must never
// reappear as allowlist exceptions (exact deleted Phase 4 symbols).
var deletedPhase4AllowlistIdentityTokens = []string{
	"requestPlaneAsBuilt",
	"RunWithRuntime",
	"NewStandardHandler",
	"standardHTTPInputFromBuilt",
	"releaseBuiltResources",
	"runClosers",
	"LegacyClosers",
	"runtimebundle.Built",
}

func identityReferencesDeletedPhase4Symbol(identity string) bool {
	for _, tok := range deletedPhase4AllowlistIdentityTokens {
		if strings.Contains(identity, tok) {
			return true
		}
	}
	// Compatibility runtimebundle.Build — not BuildBootstrap / BuildHost.
	if strings.Contains(identity, "runtimebundle.Build") &&
		!strings.Contains(identity, "runtimebundle.BuildBootstrap") &&
		!strings.Contains(identity, "runtimebundle.BuildHost") {
		return true
	}
	if identity == "type:Built" || strings.HasPrefix(identity, "type:Built.") {
		return true
	}
	return false
}

func allowlistEntryViolatesTask44Retirement(e convergenceAllowlistEntry) string {
	if permanentlyZeroToleranceAllowlistGates[e.Gate] {
		return fmt.Sprintf("gate %q is permanently zero-tolerance after Task 4.4", e.Gate)
	}
	// Fail closed: retirement_task must parse as a non-negative X.Y task ID.
	// Malformed values must not bypass the Phase 4 floor via parse failure.
	if _, _, ok := parseSpecTaskID(e.RetirementTask); !ok {
		return fmt.Sprintf("retirement_task %q is malformed (want non-negative X.Y task ID)", e.RetirementTask)
	}
	if retirementTaskAtMost(e.RetirementTask, task44AllowlistRetirementFloor) {
		return fmt.Sprintf("retirement_task %q is Phase 4 or earlier (floor %s)", e.RetirementTask, task44AllowlistRetirementFloor)
	}
	if identityReferencesDeletedPhase4Symbol(e.Identity) {
		return fmt.Sprintf("identity %q references a deleted Phase 4 symbol", e.Identity)
	}
	return ""
}

func validateAllowlistTask44Retirement(entries []convergenceAllowlistEntry) []string {
	var bad []string
	for i, e := range entries {
		if reason := allowlistEntryViolatesTask44Retirement(e); reason != "" {
			bad = append(bad, fmt.Sprintf("allowlist[%d] %s: %s", i, e.key(), reason))
		}
	}
	return bad
}

// scanDeletedSymbolSource runs every Task 4.4 composed scanner against one
// synthetic or production source unit and returns the aggregated findings.
func scanDeletedSymbolSource(filename, src string) ([]convergenceFinding, error) {
	var out []convergenceFinding
	for _, s := range deletedSymbolScanners {
		fs, err := s.Scan(filename, src)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.Name, err)
		}
		for _, f := range fs {
			if f.Gate == "" {
				f.Gate = s.Gate
			}
			out = append(out, f)
		}
	}
	return out, nil
}

func parseSpecTaskID(s string) (major, minor int, ok bool) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	if major < 0 || minor < 0 {
		return 0, 0, false
	}
	return major, minor, true
}

// retirementTaskAtMost reports whether task is numerically at or below ceiling
// (both must be "X.Y" spec task IDs).
func retirementTaskAtMost(task, ceiling string) bool {
	tm, tn, ok1 := parseSpecTaskID(task)
	cm, cn, ok2 := parseSpecTaskID(ceiling)
	if !ok1 || !ok2 {
		return false
	}
	if tm != cm {
		return tm < cm
	}
	return tn <= cn
}
