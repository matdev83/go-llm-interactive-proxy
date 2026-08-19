package capsule

import (
	"errors"
	"strings"
	"testing"
)

func testCapsule(t *testing.T) Envelope {
	t.Helper()
	branch, err := NewBranchBinding("session-parent", "a-parent", "principal")
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(branch)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestSealCanonicalDigestAndParentBinding(t *testing.T) {
	t.Parallel()
	e := testCapsule(t)
	if err := e.Verify(); err != nil {
		t.Fatalf("sealed capsule does not verify: %v", err)
	}
	if !strings.HasPrefix(e.ContentDigest, "sha256:") {
		t.Fatalf("digest = %q", e.ContentDigest)
	}
	clone := e.Clone()
	clone.Plan.Steps = append(clone.Plan.Steps, PlanStep{ID: StableStepID("keep branch"), Text: "keep branch", Status: StepPending, SourceRef: "test"})
	if err := clone.Verify(); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("mutated capsule error = %v, want digest mismatch", err)
	}
	other, err := NewBranchBinding("session-child", "a-child", "principal")
	if err != nil {
		t.Fatal(err)
	}
	if other == e.BranchBinding {
		t.Fatal("child branch unexpectedly shared parent binding")
	}
	if err := e.VerifyBranch(other); !errors.Is(err, ErrBranchMismatch) {
		t.Fatalf("wrong branch error = %v", err)
	}
}

func TestMergeExplicitCorrectionSupersedesSemanticAndDedupes(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	semantic := Delta{BaseRevision: 1, BranchBinding: base.BranchBinding, Plan: &base.Plan, Decisions: []Decision{{
		ID: StableID("decision", "billing", "automatic"), ConflictKey: "architecture.billing.mode", Statement: "Use automatic billing", Status: DecisionActive, Authority: AuthoritySemantic, SourceRef: "semantic:1",
	}}}
	first, err := Merge(base, semantic)
	if err != nil {
		t.Fatal(err)
	}
	oldID := first.Decisions[0].ID
	correction := Delta{BaseRevision: first.Revision, BranchBinding: first.BranchBinding, Decisions: []Decision{{
		ID: StableID("decision", "billing", "manual"), ConflictKey: "architecture.billing.mode", Supersedes: []string{oldID}, Statement: "Use manual billing", Status: DecisionActive, Authority: AuthorityUserExplicit, SourceRef: "user:2",
	}}}
	second, err := Merge(first, correction)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 3 {
		t.Fatalf("revision = %d", second.Revision)
	}
	active := 0
	for _, d := range second.Decisions {
		if d.Status == DecisionActive {
			active++
			if d.Statement != "Use manual billing" {
				t.Fatalf("active statement = %q", d.Statement)
			}
		}
	}
	if active != 1 {
		t.Fatalf("active decisions = %d", active)
	}
	// A retry with the same ID/content is a no-op revision merge, not a second fact.
	var activeDecision Decision
	for _, d := range second.Decisions {
		if d.Status == DecisionActive {
			activeDecision = d
		}
	}
	retry, err := Merge(second, Delta{BaseRevision: second.Revision, BranchBinding: second.BranchBinding, Decisions: []Decision{{
		ID: activeDecision.ID, ConflictKey: activeDecision.ConflictKey, Supersedes: activeDecision.Supersedes, Statement: activeDecision.Statement, Status: activeDecision.Status, Authority: activeDecision.Authority, Rationale: activeDecision.Rationale, SourceRef: activeDecision.SourceRef,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.Decisions) != len(second.Decisions) {
		t.Fatalf("dedupe changed decision count %d -> %d", len(second.Decisions), len(retry.Decisions))
	}
}

func TestMergeRejectsStaleBranchAndUnknownSupersedes(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	if _, err := Merge(base, Delta{BaseRevision: 0, BranchBinding: base.BranchBinding}); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale error = %v", err)
	}
	if _, err := Merge(base, Delta{BaseRevision: 1, BranchBinding: base.BranchBinding, Decisions: []Decision{{ID: "new", ConflictKey: "x", Supersedes: []string{"missing"}, Statement: "x", Status: DecisionActive, Authority: AuthorityUserExplicit}}}); !errors.Is(err, ErrUnknownSupersede) {
		t.Fatalf("unknown supersedes error = %v", err)
	}
}

func TestMergeLosingCandidateLeavesAllSupersededTargetsUnchanged(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	semantic := Decision{
		ID: StableID("decision", "low-target"), ConflictKey: "slot.low", Statement: "semantic choice", Status: DecisionActive, Authority: AuthoritySemantic,
	}
	explicit := Decision{
		ID: StableID("decision", "high-target"), ConflictKey: "slot.high", Statement: "explicit choice", Status: DecisionActive, Authority: AuthorityUserExplicit,
	}
	withTargets, err := Merge(base, Delta{BaseRevision: base.Revision, BranchBinding: base.BranchBinding, Decisions: []Decision{semantic, explicit}})
	if err != nil {
		t.Fatal(err)
	}
	loser := Decision{
		ID: StableID("decision", "loser"), ConflictKey: "slot.new", Supersedes: []string{semantic.ID, explicit.ID}, Statement: "structured guess", Status: DecisionActive, Authority: AuthorityStructured,
	}
	got, err := Merge(withTargets, Delta{BaseRevision: withTargets.Revision, BranchBinding: withTargets.BranchBinding, Decisions: []Decision{loser}})
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range got.Decisions {
		switch decision.ID {
		case semantic.ID, explicit.ID:
			if decision.Status != DecisionActive {
				t.Fatalf("losing candidate changed prior decision %q to %q", decision.ID, decision.Status)
			}
		case loser.ID:
			if decision.Status != DecisionSuperseded {
				t.Fatalf("losing candidate status = %q", decision.Status)
			}
		}
	}
}

func TestMergeRejectsForwardSupersedesWithinOneDelta(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	first := Decision{
		ID: StableID("decision", "same-delta-first"), ConflictKey: "slot.first", Statement: "first", Status: DecisionActive, Authority: AuthorityUserExplicit,
	}
	forward := Decision{
		ID: StableID("decision", "same-delta-forward"), ConflictKey: "slot.second", Supersedes: []string{first.ID}, Statement: "forward", Status: DecisionActive, Authority: AuthorityUserExplicit,
	}
	if _, err := Merge(base, Delta{BaseRevision: base.Revision, BranchBinding: base.BranchBinding, Decisions: []Decision{first, forward}}); !errors.Is(err, ErrUnknownSupersede) {
		t.Fatalf("same-delta supersedes error = %v, want %v", err, ErrUnknownSupersede)
	}
}

func TestPlanProgressDoesNotRegressAndPruneRedigests(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	step := PlanStep{ID: StableStepID("ship"), Text: "ship", Status: StepPending, SourceRef: "structured:1"}
	progress, err := Merge(base, Delta{BaseRevision: 1, BranchBinding: base.BranchBinding, Plan: &Plan{Status: PlanAccepted, Source: SourceStructured, Steps: []PlanStep{step}}})
	if err != nil {
		t.Fatal(err)
	}
	step.Status = StepCompleted
	done, err := Merge(progress, Delta{BaseRevision: progress.Revision, BranchBinding: progress.BranchBinding, Plan: &Plan{Status: PlanAccepted, Source: SourceStructured, Steps: []PlanStep{step}}})
	if err != nil {
		t.Fatal(err)
	}
	step.Status = StepPending
	regressed, err := Merge(done, Delta{BaseRevision: done.Revision, BranchBinding: done.BranchBinding, Plan: &Plan{Status: PlanAccepted, Source: SourceStructured, Steps: []PlanStep{step}}})
	if err != nil {
		t.Fatal(err)
	}
	if regressed.Plan.Steps[0].Status != StepCompleted {
		t.Fatalf("step regressed to %q", regressed.Plan.Steps[0].Status)
	}
	pruned, err := Prune(regressed, len(mustMarshal(regressed))-1)
	if err != nil {
		t.Fatal(err)
	}
	if err := pruned.Verify(); err != nil {
		t.Fatalf("pruned digest invalid: %v", err)
	}
	if len(pruned.Plan.Steps) != 0 {
		t.Fatalf("completed step was not pruned: %#v", pruned.Plan.Steps)
	}
}

func TestParseRejectsMalformedUnknownAndDigestMismatch(t *testing.T) {
	t.Parallel()
	e := testCapsule(t)
	b, err := marshalCanonical(e)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.Replace(string(b), `"schema_version":1`, `"schema_version":2`, 1))
	if _, err := Parse(b); !errors.Is(err, ErrInvalidCapsule) {
		t.Fatalf("unknown schema error = %v", err)
	}
	b, _ = marshalCanonical(e)
	b = []byte(strings.Replace(string(b), `"content_digest":"`+e.ContentDigest+`"`, `"content_digest":"sha256:`+strings.Repeat("0", 64)+`"`, 1))
	if _, err := Parse(b); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("digest mismatch error = %v", err)
	}
	if _, err := Parse([]byte(`{"schema_version":1}`)); !errors.Is(err, ErrInvalidCapsule) {
		t.Fatalf("malformed capsule error = %v", err)
	}
	b, _ = marshalCanonical(e)
	if _, err := Parse(append(b, []byte(` {}`)...)); !errors.Is(err, ErrInvalidCapsule) {
		t.Fatalf("trailing JSON error = %v", err)
	}
}
