package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteGeneratedPairAtomic_Success verifies successful replacement of both files.
func TestWriteGeneratedPairAtomic_Success(t *testing.T) {
	t.Parallel()

	dir1 := t.TempDir()
	dir2 := t.TempDir()

	path1 := filepath.Join(dir1, "plane_generated.go")
	path2 := filepath.Join(dir2, "bundle_projection_generated.go")

	require.NoError(t, os.WriteFile(path1, []byte("// orig1\n"), 0o644))
	require.NoError(t, os.WriteFile(path2, []byte("// orig2\n"), 0o644))

	newContent1 := []byte("// new1\n")
	newContent2 := []byte("// new2\n")

	err := WriteGeneratedPairAtomic(path1, newContent1, path2, newContent2)
	require.NoError(t, err)

	got1, err := os.ReadFile(path1)
	require.NoError(t, err)
	assert.Equal(t, string(newContent1), string(got1))

	got2, err := os.ReadFile(path2)
	require.NoError(t, err)
	assert.Equal(t, string(newContent2), string(got2))

	// Verify temporary files are cleaned
	entries1, err := os.ReadDir(dir1)
	require.NoError(t, err)
	assert.Len(t, entries1, 1)

	entries2, err := os.ReadDir(dir2)
	require.NoError(t, err)
	assert.Len(t, entries2, 1)
}

// TestWriteGeneratedPairAtomic_SecondInstallFailureRollback proves that failure during second install
// restores the first file to its original content and leaves the second file in its original state.
func TestWriteGeneratedPairAtomic_SecondInstallFailureRollback(t *testing.T) {
	t.Parallel()

	dir1 := t.TempDir()
	dir2 := t.TempDir()

	path1 := filepath.Join(dir1, "plane_generated.go")
	path2 := filepath.Join(dir2, "bundle_projection_generated.go")

	origContent1 := []byte("// orig1\n")
	origContent2 := []byte("// orig2\n")

	require.NoError(t, os.WriteFile(path1, origContent1, 0o644))
	require.NoError(t, os.WriteFile(path2, origContent2, 0o644))

	newContent1 := []byte("// new1\n")
	newContent2 := []byte("// new2\n")

	// Inject rename failure on path2
	simulatedErr := os.ErrPermission
	mockOps := pairWriteOps{
		rename: func(oldpath, newpath string) error {
			if strings.Contains(oldpath, ".tmp-") && newpath == path2 {
				return simulatedErr
			}
			return os.Rename(oldpath, newpath)
		},
	}

	err := writeGeneratedPairAtomicInternal(path1, newContent1, path2, newContent2, mockOps)
	require.Error(t, err)
	assert.ErrorIs(t, err, simulatedErr)

	// First file must be rolled back to origContent1
	got1, err := os.ReadFile(path1)
	require.NoError(t, err)
	assert.Equal(t, string(origContent1), string(got1))

	// Second file must still be origContent2
	got2, err := os.ReadFile(path2)
	require.NoError(t, err)
	assert.Equal(t, string(origContent2), string(got2))

	// Temporary files must be cleaned up in both directories
	entries1, err := os.ReadDir(dir1)
	require.NoError(t, err)
	assert.Len(t, entries1, 1, "dir1 should have only plane_generated.go, no leftovers")

	entries2, err := os.ReadDir(dir2)
	require.NoError(t, err)
	assert.Len(t, entries2, 1, "dir2 should have only bundle_projection_generated.go, no leftovers")
}

// TestPlaneGenerator_PrivilegeAcceptedForms verifies that exact string literals and bare identifiers are accepted.
func TestPlaneGenerator_PrivilegeAcceptedForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		manifest string
	}{
		{
			name: "bare canonical identifier accepted",
			manifest: `package feature
import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
var PlaneA = Plane[[]toolpolicy.Policy]{
	ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{
		StageID: StageIDToolEventReaction, Order: 10,
		Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil },
		Privileges: func(v []toolpolicy.Policy) PrivilegeProjection {
			return PrivilegeProjection{Flags: []string{PrivilegeRawCapture, PrivilegeAuxiliaryRequests, PrivilegeAuthProvider, PrivilegeCompletionGate}}
		},
	},
}
var StandardPlanes = []any{PlaneA}
`,
		},
		{
			name: "exact canonical literal accepted",
			manifest: `package feature
import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
var PlaneA = Plane[[]toolpolicy.Policy]{
	ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{
		StageID: StageIDToolEventReaction, Order: 10,
		Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil },
		Privileges: func(v []toolpolicy.Policy) PrivilegeProjection {
			return PrivilegeProjection{Flags: []string{"raw_capture", "auxiliary_requests", "auth_provider", "completion_gate"}}
		},
	},
}
var StandardPlanes = []any{PlaneA}
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := GenerateFeaturePlanesCode([]byte(tt.manifest))
			require.NoError(t, err)
		})
	}
}
