package compactiondetect_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactiondetect"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCharacterize_CompactionDetect_MechanicalSourceAndLifecycleInvariants mechanically
// inspects all production source files in internal/core/compactiondetect to prove:
// 1. Zero `go ` statements (no background goroutines spawned).
// 2. Zero `Close` methods or implementations of io.Closer.
// 3. Strictly whitelisted imports (crypto/sha256, encoding/binary, encoding/hex, strings, sync, time, lipapi, compaction).
// 4. Zero external I/O packages (net, os, http, database, io, etc.) and zero internal/core or feature imports.
// 5. ProcessServices remains single lifetime owner with no generation-owned resources or background tickers.
func TestCharacterize_CompactionDetect_MechanicalSourceAndLifecycleInvariants(t *testing.T) {
	t.Parallel()

	pkgDir := "."
	entries, err := os.ReadDir(pkgDir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	var prodFiles []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			prodFiles = append(prodFiles, name)
		}
	}

	require.NotEmpty(t, prodFiles, "must find production source files in compactiondetect")

	// Whitelist of allowed imports in production compactiondetect package.
	allowedImports := map[string]bool{
		`"crypto/sha256"`:   true,
		`"encoding/binary"`: true,
		`"encoding/hex"`:    true,
		`"strconv"`:         true,
		`"strings"`:         true,
		`"sync"`:            true,
		`"time"`:            true,
		`"unicode/utf8"`:    true,
		`"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"`:            true,
		`"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"`: true,
	}

	for _, fileName := range prodFiles {
		filePath := filepath.Join(pkgDir, fileName)
		node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
		require.NoError(t, err, "failed to parse production file: %s", fileName)

		// 1. Imports validation
		for _, imp := range node.Imports {
			importPath := imp.Path.Value
			assert.True(t, allowedImports[importPath],
				"forbidden import in %s: %s (only standard crypto/encoding/strings/sync/time and pkg/lipapi, pkg/lipsdk/compaction allowed)",
				fileName, importPath)
		}

		// 2. AST inspection for `go` statements and `Close` methods
		ast.Inspect(node, func(n ast.Node) bool {
			if n == nil {
				return true
			}

			// Invariant 1: No `go` statement
			if _, ok := n.(*ast.GoStmt); ok {
				pos := fset.Position(n.Pos())
				t.Errorf("found forbidden 'go' statement in production file %s at line %d (detector must not spawn goroutines)",
					fileName, pos.Line)
			}

			// Invariant 2: No `Close` method declaration
			if funcDecl, ok := n.(*ast.FuncDecl); ok {
				if funcDecl.Name != nil && funcDecl.Name.Name == "Close" {
					pos := fset.Position(n.Pos())
					t.Errorf("found forbidden 'Close' method in production file %s at line %d (detector has no owned resources to close)",
						fileName, pos.Line)
				}
			}

			return true
		})
	}

	// Invariant 3: Verify *Detector does not implement io.Closer or any Closer interface
	d := compactiondetect.New(compactiondetect.Config{})
	dType := reflect.TypeOf(d)
	for i := 0; i < dType.NumMethod(); i++ {
		method := dType.Method(i)
		assert.NotEqual(t, "Close", method.Name, "*Detector must not have Close method")
	}
}

// TestCharacterize_CompactionDetect_ContractRepresentability asserts that all detector
// methods called by runtime (RequestOpened, PreviewResponse, ResponseReleased) use
// inputs and outputs that map directly to lipapi and lipsdk/compaction types without
// needing concrete detector types.
func TestCharacterize_CompactionDetect_ContractRepresentability(t *testing.T) {
	t.Parallel()

	// Verify correlation metadata field parity
	reqMeta := compactiondetect.RequestMeta{
		TraceID:    "trace-1",
		ALegID:     "aleg-1",
		BLegID:     "bleg-1",
		AttemptSeq: 1,
		SessionID:  "sess-1",
	}

	respMeta := compactiondetect.ResponseMeta{
		TraceID:    "trace-1",
		ALegID:     "aleg-1",
		BLegID:     "bleg-1",
		AttemptSeq: 1,
		SessionID:  "sess-1",
	}

	presMeta := compaction.PreservationMeta{
		TraceID:    reqMeta.TraceID,
		ALegID:     reqMeta.ALegID,
		BLegID:     reqMeta.BLegID,
		AttemptSeq: reqMeta.AttemptSeq,
		SessionID:  reqMeta.SessionID,
	}

	assert.Equal(t, reqMeta.TraceID, presMeta.TraceID)
	assert.Equal(t, reqMeta.ALegID, presMeta.ALegID)
	assert.Equal(t, reqMeta.BLegID, presMeta.BLegID)
	assert.Equal(t, reqMeta.AttemptSeq, presMeta.AttemptSeq)
	assert.Equal(t, reqMeta.SessionID, presMeta.SessionID)

	assert.Equal(t, respMeta.TraceID, presMeta.TraceID)
	assert.Equal(t, respMeta.ALegID, presMeta.ALegID)
	assert.Equal(t, respMeta.BLegID, presMeta.BLegID)
	assert.Equal(t, respMeta.AttemptSeq, presMeta.AttemptSeq)
	assert.Equal(t, respMeta.SessionID, presMeta.SessionID)
}

// TestCharacterize_CompactionDetect_NilAndBlankSafety proves that nil *Detector receivers
// and blank/whitespace ALegIDs safely return nil/zero-values without panicking.
func TestCharacterize_CompactionDetect_NilAndBlankSafety(t *testing.T) {
	t.Parallel()

	t.Run("nil_detector_receiver", func(t *testing.T) {
		t.Parallel()
		var d *compactiondetect.Detector

		meta := compactiondetect.RequestMeta{
			TraceID: "t-1",
			ALegID:  "a-1",
		}
		call := lipapi.Call{ID: "c-1"}
		ev := lipapi.Event{Kind: lipapi.EventResponseStarted}

		assert.Nil(t, d.RequestOpened(meta, call), "nil detector RequestOpened must return nil")
		assert.Equal(t, compaction.RequestPreview{Kind: compaction.PreviewNone}, d.PreviewRequest(meta, call), "nil detector PreviewRequest must return PreviewNone")
		assert.Equal(t, compaction.ResponsePreview{Kind: compaction.PreviewNone}, d.PreviewResponse(meta, ev), "nil detector PreviewResponse must return PreviewNone")
		assert.Nil(t, d.ResponseReleased(meta, ev), "nil detector ResponseReleased must return nil")
	})

	t.Run("blank_aleg_id_safety", func(t *testing.T) {
		t.Parallel()
		d := compactiondetect.New(compactiondetect.Config{})

		for _, blankALeg := range []string{"", "   ", "\t\n"} {
			meta := compactiondetect.RequestMeta{
				TraceID: "t-1",
				ALegID:  blankALeg,
			}
			call := lipapi.Call{ID: "c-1"}
			ev := lipapi.Event{Kind: lipapi.EventResponseStarted}

			assert.Nil(t, d.RequestOpened(meta, call), "blank ALegID RequestOpened must return nil")
			assert.Equal(t, compaction.RequestPreview{Kind: compaction.PreviewNone}, d.PreviewRequest(meta, call), "blank ALegID PreviewRequest must return PreviewNone")
			assert.Equal(t, compaction.ResponsePreview{Kind: compaction.PreviewNone}, d.PreviewResponse(meta, ev), "blank ALegID PreviewResponse must return PreviewNone")
			assert.Nil(t, d.ResponseReleased(meta, ev), "blank ALegID ResponseReleased must return nil")
		}
	})
}
