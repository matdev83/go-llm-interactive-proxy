package injection

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestInjectLegacyAuthorityAddsOneProxyInstruction(t *testing.T) {
	e := testCapsule(t)
	call := lipapi.Call{Messages: []lipapi.Message{{
		Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("continue")},
	}}}

	out, err := Inject(Input{
		Call: call, Capsule: e, ExpectedBranchBinding: e.BranchBinding,
		BoundaryKey: "txn-1", Limits: ProjectionLimits{MaxBytes: 16_384, MaxTokens: 4_096},
	})
	if err != nil {
		t.Fatalf("Inject() error = %v", err)
	}
	if !out.Applied || out.Marker != (Marker{BranchBinding: e.BranchBinding, BoundaryKey: "txn-1", CapsuleRevision: e.Revision}) {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	if out.Call.Items != nil || len(out.Call.Messages) != 1 || len(out.Call.Instructions) != 1 {
		t.Fatalf("legacy authority changed incorrectly: %#v", out.Call)
	}
	if got := out.Call.Instructions[0].Role; got != lipapi.RoleDeveloper && got != lipapi.RoleSystem {
		t.Fatalf("instruction role = %q, want developer or system", got)
	}
	text := out.Call.Instructions[0].Parts[0].Text
	if !strings.Contains(text, "prior continuation state") || strings.Contains(text, e.BranchBinding) || strings.Contains(text, e.ContentDigest) {
		t.Fatalf("unsafe or incomplete projection: %q", text)
	}
	if err := out.Call.Validate(); err != nil {
		t.Fatalf("injected call invalid: %v", err)
	}
}

func TestInjectItemAuthorityAddsOneCanonicalMessageItem(t *testing.T) {
	e := testCapsule(t)
	call := lipapi.Call{Items: []lipapi.Item{{
		Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser,
		Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "continue"}},
	}}}

	out, err := Inject(Input{
		Call: call, Capsule: e, ExpectedBranchBinding: e.BranchBinding,
		BoundaryKey: "txn-1", Limits: ProjectionLimits{MaxBytes: 16_384, MaxTokens: 4_096},
	})
	if err != nil {
		t.Fatalf("Inject() error = %v", err)
	}
	if out.Call.Messages != nil || out.Call.Instructions != nil || len(out.Call.Items) != 2 {
		t.Fatalf("item authority changed incorrectly: %#v", out.Call)
	}
	added := out.Call.Items[1]
	if added.Kind != lipapi.ItemKindMessage || (added.Role != lipapi.RoleDeveloper && added.Role != lipapi.RoleSystem) || len(added.Content) != 1 || added.Content[0].Kind != lipapi.ContentPartText {
		t.Fatalf("unexpected injected item: %#v", added)
	}
	if err := out.Call.Validate(); err != nil {
		t.Fatalf("injected call invalid: %v", err)
	}
}

func TestInjectExactlyOnceForCallLocalBoundaryRevisionMarker(t *testing.T) {
	e := testCapsule(t)
	base := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}}
	first, err := Inject(Input{Call: base, Capsule: e, ExpectedBranchBinding: e.BranchBinding, BoundaryKey: "txn-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Inject(Input{Call: first.Call, Capsule: e, ExpectedBranchBinding: e.BranchBinding, BoundaryKey: "txn-1", Marker: first.Marker})
	if err != nil {
		t.Fatalf("second Inject() error = %v", err)
	}
	if second.Applied || second.Marker != first.Marker || !reflect.DeepEqual(second.Call, first.Call) {
		t.Fatalf("same marker was not a no-op: first=%+v second=%+v", first, second)
	}
}

func TestInjectSameRevisionDifferentBoundariesAreIndependent(t *testing.T) {
	e := testCapsule(t)
	base := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}}
	first, err := Inject(Input{Call: base, Capsule: e, ExpectedBranchBinding: e.BranchBinding, BoundaryKey: "txn-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Inject(Input{Call: base, Capsule: e, ExpectedBranchBinding: e.BranchBinding, BoundaryKey: "txn-2", Marker: first.Marker})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Applied || second.Marker.BoundaryKey != "txn-2" || len(second.Call.Instructions) != 1 {
		t.Fatalf("distinct boundary was suppressed: %+v", second)
	}
}

func TestInjectRejectsWrongBranchAndDigestWithoutMutation(t *testing.T) {
	e := testCapsule(t)
	call := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}}
	before := lipapi.CloneCall(call)

	wrongBranch := e
	wrongBranch.BranchBinding, _ = capsule.NewBranchBinding("other", "a", "")
	wrongBranch.Seal()
	got, err := Inject(Input{Call: call, Capsule: wrongBranch, ExpectedBranchBinding: e.BranchBinding, BoundaryKey: "txn"})
	if err == nil || !reflect.DeepEqual(got.Call, call) || got.Applied || got.Marker != (Marker{}) {
		t.Fatalf("wrong branch did not roll back: got=%+v err=%v", got, err)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("wrong branch mutated input call")
	}

	wrongDigest := e
	wrongDigest.ContentDigest = strings.Repeat("0", 71)
	got, err = Inject(Input{Call: call, Capsule: wrongDigest, ExpectedBranchBinding: e.BranchBinding, BoundaryKey: "txn"})
	if err == nil || !reflect.DeepEqual(got.Call, call) || got.Applied || got.Marker != (Marker{}) {
		t.Fatalf("wrong digest did not roll back: got=%+v err=%v", got, err)
	}
}

func TestInjectPreservesOpaqueAndEncryptedItemsByteForByte(t *testing.T) {
	e := testCapsule(t)
	opaque := []byte(`{"provider":"opaque","bytes":[1,2,3]}`)
	call := lipapi.Call{Items: []lipapi.Item{
		{Kind: lipapi.ItemKindCompaction, Compaction: &lipapi.CompactionItem{
			EncryptedContent: "ciphertext==", Opaque: opaque,
		}},
		{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "x"}}},
	}}
	before := append([]byte(nil), call.Items[0].Compaction.Opaque...)
	out, err := Inject(Input{Call: call, Capsule: e, ExpectedBranchBinding: e.BranchBinding, BoundaryKey: "txn"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Call.Items[0].Compaction.Opaque, before) || out.Call.Items[0].Compaction.EncryptedContent != "ciphertext==" {
		t.Fatalf("opaque/encrypted item changed: %#v", out.Call.Items[0].Compaction)
	}
	if !bytes.Equal(call.Items[0].Compaction.Opaque, before) {
		t.Fatal("input opaque item was mutated")
	}
}

func TestInjectBudgetFailureReturnsExactCallAndNoMarker(t *testing.T) {
	e := testCapsule(t)
	call := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}}
	got, err := Inject(Input{Call: call, Capsule: e, ExpectedBranchBinding: e.BranchBinding, BoundaryKey: "txn", Limits: ProjectionLimits{MaxBytes: 1, MaxTokens: 1}})
	if err == nil || !errors.Is(err, ErrProjectionBudget) || !reflect.DeepEqual(got.Call, call) || got.Applied || got.Marker != (Marker{}) {
		t.Fatalf("budget failure did not roll back: got=%+v err=%v", got, err)
	}
}

func TestInjectPreservesNonNilEmptyAuthoritySlices(t *testing.T) {
	e := testCapsule(t)
	itemCall := lipapi.Call{Items: []lipapi.Item{}, PreviousResponseID: "resp-previous"}
	legacyCall := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}, Instructions: []lipapi.Message{}}

	itemOut, err := Inject(Input{Call: itemCall, Capsule: e, ExpectedBranchBinding: e.BranchBinding, BoundaryKey: "item"})
	if err != nil {
		t.Fatal(err)
	}
	if itemOut.Call.Items == nil || itemOut.Call.Messages != nil || itemOut.Call.Instructions != nil {
		t.Fatalf("item authority nilness changed: %#v", itemOut.Call)
	}
	legacyOut, err := Inject(Input{Call: legacyCall, Capsule: e, ExpectedBranchBinding: e.BranchBinding, BoundaryKey: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if legacyOut.Call.Items != nil || legacyOut.Call.Messages == nil || legacyOut.Call.Instructions == nil {
		t.Fatalf("legacy authority nilness changed: %#v", legacyOut.Call)
	}
}

func TestSerializeBlockIsDeterministicAndDoesNotExposeBindingMetadata(t *testing.T) {
	e := testCapsule(t)
	a, err := SerializeBlock(e, e.BranchBinding, ProjectionLimits{MaxBytes: 16_384, MaxTokens: 4_096})
	if err != nil {
		t.Fatal(err)
	}
	b, err := SerializeBlock(e, e.BranchBinding, ProjectionLimits{MaxBytes: 16_384, MaxTokens: 4_096})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || !bytes.HasPrefix(a, []byte(BlockStart)) || !bytes.HasSuffix(a, []byte(BlockEnd)) {
		t.Fatalf("serialization is not stable/delimited: %q %q", a, b)
	}
	if bytes.Contains(a, []byte(e.BranchBinding)) || bytes.Contains(a, []byte(e.ContentDigest)) {
		t.Fatalf("internal binding metadata leaked: %q", a)
	}
}

func TestTokenEquivalentUsesConservativeOneByteConvention(t *testing.T) {
	const input = "éx"
	if got, want := TokenEquivalent([]byte(input)), len([]byte(input)); got != want {
		t.Fatalf("TokenEquivalent(%q) = %d, want conservative byte count %d", input, got, want)
	}
}

func testCapsule(t *testing.T) capsule.Envelope {
	t.Helper()
	binding, err := capsule.NewBranchBinding("session", "a-leg", "")
	if err != nil {
		t.Fatal(err)
	}
	e, err := capsule.New(binding)
	if err != nil {
		t.Fatal(err)
	}
	e.Revision = 7
	e.Plan.Steps = []capsule.PlanStep{{ID: "step-1", Text: "finish the accepted plan", Status: capsule.StepInProgress, SourceRef: "user"}}
	e.Decisions = []capsule.Decision{{ID: "decision-1", ConflictKey: "mode", Statement: "Use the bounded path", Status: capsule.DecisionActive, Authority: capsule.AuthorityUserExplicit, Rationale: "safety", SourceRef: "user"}}
	e.Constraints = []capsule.Fact{{ID: "constraint-1", Statement: "Do not expose secrets", Status: capsule.FactActive, Authority: capsule.AuthorityUserExplicit}}
	if err := e.Seal(); err != nil {
		t.Fatal(err)
	}
	return e
}
