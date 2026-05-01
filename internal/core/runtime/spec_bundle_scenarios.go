package runtime

// OrchestrationScenarioSpec links a stable scenario identifier to steering-level invariants
// and the primary regression test that exercises it (specification bundle).
type OrchestrationScenarioSpec struct {
	ID               string
	InvariantSummary string
	TestName         string // existing *_test.go function name in package runtime_test
}

// SpecBundleOrchestrationScenarios lists core-owned orchestration invariants. Keep aligned
// with .kiro/steering/routing-and-orchestration.md and the referenced tests.
func SpecBundleOrchestrationScenarios() []OrchestrationScenarioSpec {
	return []OrchestrationScenarioSpec{
		{
			ID:               "SB-ORCH-pre-output-recoverable",
			InvariantSummary: "Pre-output recoverable failures may advance route candidates and record attempt lineage.",
			TestName:         "TestExecutor_preOutputRecoverableSwallowsAndLineage",
		},
		{
			ID:               "SB-ORCH-pre-output-multi-candidate",
			InvariantSummary: "Multiple pre-output failures can consume candidates until one backend succeeds.",
			TestName:         "TestExecutor_preOutputMultiOpenFailuresThenSuccess",
		},
		{
			ID:               "SB-ORCH-no-failover-after-output",
			InvariantSummary: "After first downstream content, failures must not open a second backend or classify as recoverable pre-output for retry.",
			TestName:         "TestExecutor_postOutputNoSecondBackendOpen",
		},
		{
			ID:               "SB-ORCH-max-attempts",
			InvariantSummary: "routing.max_attempts stops further B-leg opens once exhausted.",
			TestName:         "TestExecutor_maxAttemptsBlocksFurtherBLegs",
		},
		{
			ID:               "SB-ORCH-cancel-records-attempt",
			InvariantSummary: "Cancellation during streaming records attempt metadata.",
			TestName:         "TestExecutor_cancellationRecordsAttempt",
		},
		{
			ID:               "SB-ORCH-weighted-first-branch",
			InvariantSummary: "[first] weighted routing persists consumed-first state on the A-leg for continuity.",
			TestName:         "TestExecutor_weightedFirstBranch_persistsConsumed",
		},
		{
			ID:               "SB-ORCH-circuit-breaker",
			InvariantSummary: "Routing health circuit breaker can skip unhealthy candidates.",
			TestName:         "TestExecutor_circuitBreakerSkipsAfterFailures",
		},
		{
			ID:               "SB-ORCH-backend-seam-b2bua",
			InvariantSummary: "Backend seam regression: pre-output recovery and no post-output failover across representative failures.",
			TestName:         "TestExecutor_backendSeamRegression",
		},
		{
			ID:               "SB-SECURE-new-session-replaces-forged",
			InvariantSummary: "Secure session: BeginTurn with a new session replaces a forged A-leg id.",
			TestName:         "TestExecutor_prepareSubmitAndALeg_secure_newSession_replacesForgedALeg",
		},
	}
}
