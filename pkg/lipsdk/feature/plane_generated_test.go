package feature_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

// TestGenerator_DeterministicTwoRunsByteIdentical tests that running the generator
// twice produces byte-identical generated output.
func TestGenerator_DeterministicTwoRunsByteIdentical(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)
	scriptPath := filepath.Join(repoRoot, "scripts", "generate-feature-planes.go")
	tmpDir := t.TempDir()
	out1Path := filepath.Join(tmpDir, "plane_generated_1.go")
	out2Path := filepath.Join(tmpDir, "plane_generated_2.go")

	// Run generator 1st time to temp output
	cmd1 := exec.Command("go", "run", scriptPath, "-out", out1Path)
	cmd1.Dir = repoRoot
	out1, err := cmd1.CombinedOutput()
	require.NoError(t, err, "run 1 failed: %s", string(out1))

	bytesRun1, err := os.ReadFile(out1Path)
	require.NoError(t, err)

	// Run generator 2nd time to temp output
	cmd2 := exec.Command("go", "run", scriptPath, "-out", out2Path)
	cmd2.Dir = repoRoot
	out2, err := cmd2.CombinedOutput()
	require.NoError(t, err, "run 2 failed: %s", string(out2))

	bytesRun2, err := os.ReadFile(out2Path)
	require.NoError(t, err)

	// Assert byte-identical output across two runs
	assert.True(t, bytes.Equal(bytesRun1, bytesRun2), "plane_generated.go must be byte-identical across two generation runs")

	// Run generator in check mode against tracked file
	cmdCheck := exec.Command("go", "run", scriptPath, "-check")
	cmdCheck.Dir = repoRoot
	outCheck, err := cmdCheck.CombinedOutput()
	require.NoError(t, err, "generator -check failed: %s", string(outCheck))

	// Verify that generator in check mode fails on corrupted file
	corruptedPath := filepath.Join(tmpDir, "corrupted.go")
	err = os.WriteFile(corruptedPath, []byte("// corrupted"), 0o644)
	require.NoError(t, err)

	cmdCorrupt := exec.Command("go", "run", scriptPath, "-check", "-out", corruptedPath)
	cmdCorrupt.Dir = repoRoot
	outCorrupt, err := cmdCorrupt.CombinedOutput()
	assert.Error(t, err, "generator -check must fail on corrupted file: %s", string(outCorrupt))
}

// TestGeneratedCode_NoForbiddenPatterns verifies that plane_generated.go contains
// no `any`, no type assertions `.(`, no `reflect`, no `unsafe`, no `map[`, and no
// key-search loops for dispatch.
func TestGeneratedCode_NoForbiddenPatterns(t *testing.T) {
	t.Parallel()
	repoRoot := findRepoRoot(t)
	targetPath := filepath.Join(repoRoot, "pkg", "lipsdk", "feature", "plane_generated.go")

	contentBytes, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	content := string(contentBytes)

	// Dissect lines to verify each forbidden pattern
	forbiddenSubstrings := []struct {
		pattern string
		reason  string
	}{
		{"any", "forbidden runtime discovery/untyped any in generated dispatch"},
		{".(", "forbidden type assertion in generated dispatch"},
		{"reflect", "forbidden reflection in generated dispatch"},
		{"unsafe", "forbidden unsafe in generated dispatch"},
		{"map[", "forbidden map lookup in generated dispatch"},
	}

	for _, tc := range forbiddenSubstrings {
		assert.False(t, strings.Contains(content, tc.pattern),
			"plane_generated.go must not contain %q: %s", tc.pattern, tc.reason)
	}

	// Verify no range loops over maps/slices for key searching in dispatch
	assert.False(t, strings.Contains(content, "range "),
		"plane_generated.go must not contain range loops for dispatch")
}

// TestGeneratedDispatch_TypedAdaptersEndToEnd tests generated typed dispatch across
// slice, scalar, exclusive interface provider, and replace-by-identity planes.
func TestGeneratedDispatch_TypedAdaptersEndToEnd(t *testing.T) {
	t.Parallel()

	cset := feature.NewContributionSet()

	// 1. Slice plane: PlaneSubmitHooks
	hook1 := dummySubmitHook{id: "hook-a", ord: 10}
	hook2 := dummySubmitHook{id: "hook-b", ord: 5}
	err := feature.Contribute(cset, feature.PlaneSubmitHooks, "plugin-hooks", []hooks.SubmitHook{hook1, hook2})
	require.NoError(t, err)

	// 2. Scalar int plane: PlaneToolCallFinalizationMaxArgsBytes
	err = feature.Contribute(cset, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-scalar-1", 4096)
	require.NoError(t, err)
	err = feature.Contribute(cset, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-scalar-2", 2048)
	require.NoError(t, err)

	// 3. Exclusive interface provider plane: PlaneTerminalDecisionProvider
	provider := dummyTerminalProvider{id: "term-provider-primary"}
	err = feature.Contribute(cset, feature.PlaneTerminalDecisionProvider, "plugin-term", terminaldecision.Provider(provider))
	require.NoError(t, err)

	// 4. Replace-by-identity plane: PlaneCompactionPreservers
	preserver1 := dummyPreserver{id: "preserver-orig"}
	err = feature.Contribute(cset, feature.PlaneCompactionPreservers, "plugin-preserver", []compaction.Preserver{preserver1})
	require.NoError(t, err)

	// Freeze snapshot
	frozen := cset.Freeze()

	// Verify slice plane retrieval
	hooksGot := feature.Get(frozen, feature.PlaneSubmitHooks)
	require.Len(t, hooksGot, 2)
	assert.Equal(t, "hook-a", hooksGot[0].ID())
	assert.Equal(t, "hook-b", hooksGot[1].ID())

	// Verify scalar reduce plane retrieval (min positive value: 2048)
	maxArgsGot := feature.Get(frozen, feature.PlaneToolCallFinalizationMaxArgsBytes)
	assert.Equal(t, 2048, maxArgsGot)

	// Verify exclusive interface provider and identity
	providerGot := feature.Get(frozen, feature.PlaneTerminalDecisionProvider)
	require.NotNil(t, providerGot)
	assert.Equal(t, "term-provider-primary", providerGot.ID())

	termID, hasID := feature.FrozenIdentity(frozen, feature.PlaneTerminalDecisionProvider)
	assert.True(t, hasID)
	assert.Equal(t, "term-provider-primary", termID)

	// Verify replace-by-identity plane
	preserversGot := feature.Get(frozen, feature.PlaneCompactionPreservers)
	require.Len(t, preserversGot, 1)
	assert.Equal(t, "preserver-orig", preserversGot[0].ID())

	preserverID, hasPreserverID := feature.FrozenIdentity(frozen, feature.PlaneCompactionPreservers)
	assert.True(t, hasPreserverID)
	assert.Equal(t, "preserver-orig", preserverID)
}

// TestGeneratedDispatch_ReplaceByIdentity verifies that Combine on replace-by-identity
// planes replaces any existing occupant with matching ID and appends replacement.
func TestGeneratedDispatch_ReplaceByIdentity(t *testing.T) {
	t.Parallel()

	// 1. CompactionPreservers
	p1 := dummyPreserver{id: "pres-1"}
	p2 := dummyPreserver{id: "pres-2"}
	p1Replacement := dummyPreserver{id: "pres-1"}

	curPres, err := feature.PlaneCompactionPreservers.Combine(feature.SourceFeature, nil, []compaction.Preserver{p1, p2})
	require.NoError(t, err)
	require.Len(t, curPres, 2)

	replacedPres, err := feature.PlaneCompactionPreservers.Combine(feature.SourceGenerationBinder, curPres, []compaction.Preserver{p1Replacement})
	require.NoError(t, err)
	require.Len(t, replacedPres, 2)
	assert.Equal(t, "pres-2", replacedPres[0].ID())
	assert.Equal(t, "pres-1", replacedPres[1].ID())

	// 2. AttemptTransforms
	x1 := dummyAttemptTransform{id: "xform-1"}
	x2 := dummyAttemptTransform{id: "xform-2"}
	x1Replacement := dummyAttemptTransform{id: "xform-1"}

	curX, err := feature.PlaneAttemptTransforms.Combine(feature.SourceFeature, nil, []request.AttemptTransform{x1, x2})
	require.NoError(t, err)
	require.Len(t, curX, 2)

	replacedX, err := feature.PlaneAttemptTransforms.Combine(feature.SourceGenerationBinder, curX, []request.AttemptTransform{x1Replacement})
	require.NoError(t, err)
	require.Len(t, replacedX, 2)
	assert.Equal(t, "xform-2", replacedX[0].ID())
	assert.Equal(t, "xform-1", replacedX[1].ID())

	// 3. StreamObserverFactories
	f1 := dummyStreamObserverFactory{id: "obs-1"}
	f2 := dummyStreamObserverFactory{id: "obs-2"}
	f1Replacement := dummyStreamObserverFactory{id: "obs-1"}

	curF, err := feature.PlaneStreamObserverFactories.Combine(feature.SourceFeature, nil, []response.StreamObserverFactory{f1, f2})
	require.NoError(t, err)
	require.Len(t, curF, 2)

	replacedF, err := feature.PlaneStreamObserverFactories.Combine(feature.SourceGenerationBinder, curF, []response.StreamObserverFactory{f1Replacement})
	require.NoError(t, err)
	require.Len(t, replacedF, 2)
	assert.Equal(t, "obs-2", replacedF[0].ID())
	assert.Equal(t, "obs-1", replacedF[1].ID())
}

// Stubs for test cases
type dummyPreserver struct {
	id string
}

func (p dummyPreserver) ID() string { return p.id }
func (p dummyPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p dummyPreserver) RequestOpened(context.Context, lipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p dummyPreserver) BeforeResponseRelease(context.Context, *lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type dummyAttemptTransform struct {
	id  string
	ord int
}

func (t dummyAttemptTransform) ID() string                     { return t.id }
func (t dummyAttemptTransform) Order() int                     { return t.ord }
func (t dummyAttemptTransform) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (t dummyAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{}, nil
}

type dummyStreamObserverFactory struct {
	id  string
	ord int
}

func (f dummyStreamObserverFactory) ID() string                     { return f.id }
func (f dummyStreamObserverFactory) Order() int                     { return f.ord }
func (f dummyStreamObserverFactory) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (f dummyStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate repository root containing go.mod")
	return ""
}

// TestGenerator_RejectsIllegalManifests verifies that the generator CLI rejects
// illegal manifests including duplicate IDs, incomplete source rules, missing combine functions,
// and invalid multiplicity before any code is generated.
func TestGenerator_RejectsIllegalManifests(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)
	scriptPath := filepath.Join(repoRoot, "scripts", "generate-feature-planes.go")

	cases := []struct {
		name        string
		manifestSrc string
		expectedErr string
	}{
		{
			name: "duplicate_plane_id",
			manifestSrc: `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

var PlaneOne = Plane[[]hooks.SubmitHook]{
	ID: "duplicate_id",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var PlaneTwo = Plane[[]hooks.SubmitHook]{
	ID: "duplicate_id",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneOne,
	PlaneTwo,
}
`,
			expectedErr: `duplicate plane ID "duplicate_id"`,
		},
		{
			name: "incomplete_source_rules",
			manifestSrc: `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

var PlaneIncomplete = Plane[[]hooks.SubmitHook]{
	ID: "incomplete_rules_plane",
	Multiplicity: MultOrdered,
	Rules: SourceRules{},
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneIncomplete,
}
`,
			expectedErr: "at least one source rule must be specified",
		},
		{
			name: "missing_combine_function",
			manifestSrc: `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

var PlaneNoCombine = Plane[[]hooks.SubmitHook]{
	ID: "no_combine_plane",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNoCombine,
}
`,
			expectedErr: "combine function is required",
		},
		{
			name: "nil_combine_function",
			manifestSrc: `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

var PlaneNilCombine = Plane[[]hooks.SubmitHook]{
	ID: "nil_combine_plane",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	Combine: nil,
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNilCombine,
}
`,
			expectedErr: "combine function is required",
		},
		{
			name: "missing_plane_id",
			manifestSrc: `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

var PlaneNoID = Plane[[]hooks.SubmitHook]{
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNoID,
}
`,
			expectedErr: "plane ID is required and must not be empty",
		},
		{
			name: "exclusive_plane_with_concatenate",
			manifestSrc: `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

var PlaneBadExclusive = Plane[[]hooks.SubmitHook]{
	ID: "bad_exclusive",
	Multiplicity: MultExclusive,
	Rules: SourceRules{Feature: CombConcatenate},
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneBadExclusive,
}
`,
			expectedErr: "exclusive plane cannot use concatenate or reduce rule",
		},
		{
			name: "exclusive_plane_without_identity",
			manifestSrc: `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"

var PlaneNoIdentityExclusive = Plane[terminaldecision.Provider]{
	ID: "exclusive_no_identity",
	Multiplicity: MultExclusive,
	Rules: SourceRules{Feature: CombExclusive},
	Combine: func(source SourceKind, cur, inc terminaldecision.Provider) (terminaldecision.Provider, error) { return inc, nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNoIdentityExclusive,
}
`,
			expectedErr: "identity function is required for exclusive or replace-by-identity plane",
		},
		{
			name: "exclusive_plane_with_nil_identity",
			manifestSrc: `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"

var PlaneNilIdentityExclusive = Plane[terminaldecision.Provider]{
	ID: "exclusive_nil_identity",
	Multiplicity: MultExclusive,
	Rules: SourceRules{Feature: CombExclusive},
	Identity: nil,
	Combine: func(source SourceKind, cur, inc terminaldecision.Provider) (terminaldecision.Provider, error) { return inc, nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNilIdentityExclusive,
}
`,
			expectedErr: "identity function is required for exclusive or replace-by-identity plane",
		},
		{
			name: "replace_by_identity_without_identity",
			manifestSrc: `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

var PlaneReplaceNoIdentity = Plane[[]hooks.SubmitHook]{
	ID: "replace_no_identity",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
		GenerationBinder: CombReplaceByIdentity,
	},
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneReplaceNoIdentity,
}
`,
			expectedErr: "identity function is required for exclusive or replace-by-identity plane",
		},
		{
			name: "diagnostics_missing_stage_id",
			manifestSrc: `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

var PlaneNoStageID = Plane[[]hooks.SubmitHook]{
	ID: "no_stage_id_plane",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
	Diagnostics: DiagnosticDescriptor[[]hooks.SubmitHook]{
		StageID: "",
		Materialize: func(v []hooks.SubmitHook) []DiagnosticOccupant { return nil },
	},
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNoStageID,
}
`,
			expectedErr: "diagnostics StageID must not be empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()
			manifestFile := filepath.Join(tmpDir, "plane_manifest.go")
			outFile := filepath.Join(tmpDir, "plane_generated.go")

			err := os.WriteFile(manifestFile, []byte(tc.manifestSrc), 0o644)
			require.NoError(t, err)

			cmd := exec.Command("go", "run", scriptPath, "-manifest", manifestFile, "-out", outFile)
			cmd.Dir = repoRoot
			out, err := cmd.CombinedOutput()
			assert.Error(t, err, "generator must fail on illegal manifest %s", tc.name)
			assert.Contains(t, string(out), tc.expectedErr, "generator output must contain expected error for %s", tc.name)
		})
	}
}
