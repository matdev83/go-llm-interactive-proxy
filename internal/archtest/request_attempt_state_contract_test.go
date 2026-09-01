package archtest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockArchtestFS struct {
	files         map[string][]byte
	dirs          map[string][]os.DirEntry
	readErr       error
	dirErr        error
	readDirCount  int
	readFileCount int
	onReadDir     func()
	onReadFile    func(rel string)
}

func (m *mockArchtestFS) ReadFile(rel string) ([]byte, error) {
	m.readFileCount++
	if m.onReadFile != nil {
		m.onReadFile(rel)
	}
	if m.readErr != nil {
		return nil, m.readErr
	}
	content, ok := m.files[filepath.ToSlash(rel)]
	if !ok {
		return nil, os.ErrNotExist
	}
	return content, nil
}

func (m *mockArchtestFS) WalkProductionGoFiles(fn func(rel string, src []byte) error) error {
	return nil
}

func (m *mockArchtestFS) WalkRootFiles(rootPath string, fn func(rel string, src []byte) error) error {
	return nil
}

func (m *mockArchtestFS) ReadDir(rel string) ([]os.DirEntry, error) {
	m.readDirCount++
	if m.onReadDir != nil {
		m.onReadDir()
	}
	if m.dirErr != nil {
		return nil, m.dirErr
	}
	return m.dirs[filepath.ToSlash(rel)], nil
}

func TestLoadTurnRecvASTFilesFromFS_Contract(t *testing.T) {
	t.Parallel()

	t.Run("filters_sorts_and_extracts_imports", func(t *testing.T) {
		t.Parallel()
		mockFS := &mockArchtestFS{
			dirs: map[string][]os.DirEntry{
				"internal/core/runtime": {
					gitDirEntry{name: "z_stream.go", isDir: false},
					gitDirEntry{name: "a_runtime.go", isDir: false},
					gitDirEntry{name: "ignored_test.go", isDir: false},
					gitDirEntry{name: "subpkg", isDir: true},
					gitDirEntry{name: "readme.txt", isDir: false},
				},
			},
			files: map[string][]byte{
				"internal/core/runtime/z_stream.go":  []byte("package runtime\n\nimport (\n\t\"context\"\n\trename \"net/http\"\n\t_ \"embed\"\n\t. \"fmt\"\n)\ntype Z struct{}\n"),
				"internal/core/runtime/a_runtime.go": []byte("package runtime\n\ntype A struct{}\n"),
			},
		}

		files, err := loadTurnRecvASTFilesFromFS(mockFS)
		if err != nil {
			t.Fatalf("loadTurnRecvASTFilesFromFS failed: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("expected 2 files, got %d", len(files))
		}
		if files[0].RelPath != "internal/core/runtime/a_runtime.go" {
			t.Errorf("files[0].RelPath = %q, want internal/core/runtime/a_runtime.go", files[0].RelPath)
		}
		if files[1].RelPath != "internal/core/runtime/z_stream.go" {
			t.Errorf("files[1].RelPath = %q, want internal/core/runtime/z_stream.go", files[1].RelPath)
		}
		if files[1].AST == nil || files[1].FSet == nil {
			t.Fatal("expected non-nil AST and FSet")
		}
		wantImports := map[string]string{
			"context": "context",
			"rename":  "net/http",
		}
		if !maps.Equal(files[1].Imports, wantImports) {
			t.Errorf("imports mismatch: got %v, want %v", files[1].Imports, wantImports)
		}
	})

	t.Run("read_dir_error", func(t *testing.T) {
		t.Parallel()
		errFS := &mockArchtestFS{
			dirErr: fmt.Errorf("simulated dir read error"),
		}
		if _, err := loadTurnRecvASTFilesFromFS(errFS); err == nil {
			t.Fatal("expected error on dir read failure, got nil")
		}
	})

	t.Run("read_file_error", func(t *testing.T) {
		t.Parallel()
		errReadFileFS := &mockArchtestFS{
			dirs: map[string][]os.DirEntry{
				"internal/core/runtime": {
					gitDirEntry{name: "file.go", isDir: false},
				},
			},
			readErr: fmt.Errorf("simulated file read error"),
		}
		if _, err := loadTurnRecvASTFilesFromFS(errReadFileFS); err == nil {
			t.Fatal("expected error on file read failure, got nil")
		}
	})

	t.Run("bad_syntax_error", func(t *testing.T) {
		t.Parallel()
		badSyntaxFS := &mockArchtestFS{
			dirs: map[string][]os.DirEntry{
				"internal/core/runtime": {
					gitDirEntry{name: "broken.go", isDir: false},
				},
			},
			files: map[string][]byte{
				"internal/core/runtime/broken.go": []byte("package runtime\n\ninvalid go code {{{"),
			},
		}
		if _, err := loadTurnRecvASTFilesFromFS(badSyntaxFS); err == nil {
			t.Fatal("expected parse error on broken syntax, got nil")
		}
	})

	t.Run("canceled_context_aborts_before_readdir", func(t *testing.T) {
		t.Parallel()
		trackingFS := &mockArchtestFS{
			dirs: map[string][]os.DirEntry{
				"internal/core/runtime": {
					gitDirEntry{name: "file.go", isDir: false},
				},
			},
			files: map[string][]byte{
				"internal/core/runtime/file.go": []byte("package runtime\n"),
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := loadTurnRecvASTFilesFromFSContext(ctx, trackingFS)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if trackingFS.readDirCount != 0 {
			t.Errorf("readDirCount = %d, want 0", trackingFS.readDirCount)
		}
		if trackingFS.readFileCount != 0 {
			t.Errorf("readFileCount = %d, want 0", trackingFS.readFileCount)
		}
	})

	t.Run("canceled_context_aborts_after_readdir", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		trackingFS := &mockArchtestFS{
			dirs: map[string][]os.DirEntry{
				"internal/core/runtime": {
					gitDirEntry{name: "file.go", isDir: false},
				},
			},
			files: map[string][]byte{
				"internal/core/runtime/file.go": []byte("package runtime\n"),
			},
			onReadDir: func() {
				cancel()
			},
		}

		_, err := loadTurnRecvASTFilesFromFSContext(ctx, trackingFS)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if trackingFS.readDirCount != 1 {
			t.Errorf("readDirCount = %d, want 1", trackingFS.readDirCount)
		}
		if trackingFS.readFileCount != 0 {
			t.Errorf("readFileCount = %d, want 0", trackingFS.readFileCount)
		}
	})
}

func TestLoadTurnRecvASTFilesAtRef_Contract(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	t.Run("head_matches_working_tree", func(t *testing.T) {
		t.Parallel()
		headFiles, err := loadTurnRecvASTFilesAtRefContext(t.Context(), root, "HEAD")
		if err != nil {
			t.Fatalf("loadTurnRecvASTFilesAtRefContext(root, HEAD) failed: %v", err)
		}
		if len(headFiles) == 0 {
			t.Fatal("expected non-empty files from HEAD")
		}

		wtFiles, err := loadTurnRecvASTFilesContext(t.Context(), root)
		if err != nil {
			t.Fatalf("loadTurnRecvASTFilesContext(root) failed: %v", err)
		}
		if len(headFiles) != len(wtFiles) {
			t.Fatalf("HEAD file count (%d) mismatch with working tree (%d)", len(headFiles), len(wtFiles))
		}

		for i := range headFiles {
			if headFiles[i].RelPath != wtFiles[i].RelPath {
				t.Errorf("file[%d] RelPath mismatch: HEAD=%q, WT=%q", i, headFiles[i].RelPath, wtFiles[i].RelPath)
			}
			if !strings.HasPrefix(headFiles[i].RelPath, "internal/core/runtime/") {
				t.Errorf("file %q does not have expected prefix", headFiles[i].RelPath)
			}
			if !strings.HasSuffix(headFiles[i].RelPath, ".go") || strings.HasSuffix(headFiles[i].RelPath, "_test.go") {
				t.Errorf("file %q should be non-test go file", headFiles[i].RelPath)
			}
			if headFiles[i].AST == nil || headFiles[i].FSet == nil {
				t.Errorf("file %q has nil AST or FSet", headFiles[i].RelPath)
			}
			if !maps.Equal(headFiles[i].Imports, wtFiles[i].Imports) {
				t.Errorf("file %q imports mismatch: HEAD=%v, WT=%v", headFiles[i].RelPath, headFiles[i].Imports, wtFiles[i].Imports)
			}
		}
	})

	t.Run("invalid_ref_fails", func(t *testing.T) {
		t.Parallel()
		_, err := loadTurnRecvASTFilesAtRefContext(t.Context(), root, "invalid-ref-contract-test-nonexistent-404")
		if err == nil {
			t.Fatal("expected error for invalid git ref, got nil")
		}
	})

	t.Run("canceled_context_fails", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := loadTurnRecvASTFilesAtRefContext(ctx, root, "HEAD")
		if err == nil {
			t.Fatal("expected error on canceled context, got nil")
		}
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}

func TestLoadGitCommitFSContext_CancellationAndCacheRecovery(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	// Cancellation test: pre-canceled context must fail immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := loadGitCommitFSContext(ctx, root, "HEAD")
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// Cache recovery test: subsequent call with valid context must succeed (not poisoned)
	fs, err := loadGitCommitFSContext(t.Context(), root, "HEAD")
	if err != nil {
		t.Fatalf("subsequent loadGitCommitFSContext failed after cancellation: %v", err)
	}
	if len(fs.files) == 0 {
		t.Fatal("expected non-empty files in loaded gitCommitFS")
	}
}
