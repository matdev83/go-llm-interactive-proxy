package archtest

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// archtestFS is an abstraction over file reading/walking to support both
// the mutable working tree and pinned Git commit trees.
type archtestFS interface {
	ReadFile(rel string) ([]byte, error)
	WalkProductionGoFiles(fn func(rel string, src []byte) error) error
	WalkRootFiles(rootPath string, fn func(rel string, src []byte) error) error
	ReadDir(rel string) ([]os.DirEntry, error)
}

// workingTreeFS reads directly from the OS working tree.
type workingTreeFS struct {
	root string
}

func (w *workingTreeFS) ReadFile(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(w.root, filepath.FromSlash(rel)))
}

func (w *workingTreeFS) WalkProductionGoFiles(fn func(rel string, src []byte) error) error {
	return WalkProductionGoFiles(w.root, func(rel, abs string, src []byte) error {
		return fn(rel, src)
	})
}

func (w *workingTreeFS) WalkRootFiles(rootPath string, fn func(rel string, src []byte) error) error {
	dir := filepath.Join(w.root, filepath.FromSlash(rootPath))
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(w.root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		src, serr := os.ReadFile(path)
		if serr != nil {
			return serr
		}
		return fn(rel, src)
	})
}

func (w *workingTreeFS) ReadDir(rel string) ([]os.DirEntry, error) {
	return os.ReadDir(filepath.Join(w.root, filepath.FromSlash(rel)))
}

// gitCommitFS reads from an in-memory map populated via git archive.
type gitCommitFS struct {
	sha   string
	files map[string][]byte
}

var (
	gitCommitFSCacheMu sync.Mutex
	gitCommitFSCache   = make(map[string]*gitCommitFSEntry)
)

type gitCommitFSEntry struct {
	done chan struct{}
	fs   *gitCommitFS
	err  error
}

func loadGitCommitFS(root string, sha string) (*gitCommitFS, error) {
	key := root + "\x00" + sha
	gitCommitFSCacheMu.Lock()
	if entry, ok := gitCommitFSCache[key]; ok {
		gitCommitFSCacheMu.Unlock()
		<-entry.done
		return entry.fs, entry.err
	}
	entry := &gitCommitFSEntry{done: make(chan struct{})}
	gitCommitFSCache[key] = entry
	gitCommitFSCacheMu.Unlock()

	defer close(entry.done)

	cmd := exec.Command("git", "-C", root, "archive", "--format=tar", sha)
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		entry.err = fmt.Errorf("git archive for SHA %s failed: %w (stderr: %s)", sha, err, errOut.String())
		return nil, entry.err
	}

	files := make(map[string][]byte)
	tr := tar.NewReader(&out)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			entry.err = err
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		var content bytes.Buffer
		if _, err := io.Copy(&content, tr); err != nil {
			entry.err = err
			return nil, err
		}
		files[filepath.ToSlash(hdr.Name)] = content.Bytes()
	}
	entry.fs = &gitCommitFS{sha: sha, files: files}
	return entry.fs, nil
}

func (g *gitCommitFS) ReadFile(rel string) ([]byte, error) {
	rel = filepath.ToSlash(rel)
	content, ok := g.files[rel]
	if !ok {
		return nil, os.ErrNotExist
	}
	return content, nil
}

func (g *gitCommitFS) WalkProductionGoFiles(fn func(rel string, src []byte) error) error {
	var paths []string
	for p := range g.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, rel := range paths {
		inProductionRoot := false
		for _, top := range ProductionScanRoots() {
			if rel == top || strings.HasPrefix(rel, top+"/") {
				inProductionRoot = true
				break
			}
		}
		if !inProductionRoot {
			continue
		}

		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			continue
		}

		skip := false
		for seg := range strings.SplitSeq(rel, "/") {
			if seg == "vendor" || seg == "testdata" || seg == "node_modules" {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		if err := fn(rel, g.files[rel]); err != nil {
			return err
		}
	}
	return nil
}

func (g *gitCommitFS) WalkRootFiles(rootPath string, fn func(rel string, src []byte) error) error {
	rootPath = filepath.ToSlash(rootPath)
	var paths []string
	for p := range g.files {
		if p == rootPath || strings.HasPrefix(p, rootPath+"/") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	for _, rel := range paths {
		if !strings.HasSuffix(rel, ".go") {
			continue
		}
		if err := fn(rel, g.files[rel]); err != nil {
			return err
		}
	}
	return nil
}

type gitDirEntry struct {
	name  string
	isDir bool
}

func (e gitDirEntry) Name() string               { return e.name }
func (e gitDirEntry) IsDir() bool                { return e.isDir }
func (e gitDirEntry) Type() os.FileMode          { return 0 }
func (e gitDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func (g *gitCommitFS) ReadDir(rel string) ([]os.DirEntry, error) {
	rel = filepath.ToSlash(rel)
	prefix := rel + "/"
	if rel == "." || rel == "" {
		prefix = ""
	}

	seenDirs := make(map[string]bool)
	seenFiles := make(map[string]bool)

	for p := range g.files {
		if prefix != "" && !strings.HasPrefix(p, prefix) {
			continue
		}
		suffix := p[len(prefix):]
		parts := strings.Split(suffix, "/")
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		if len(parts) > 1 {
			seenDirs[parts[0]] = true
		} else {
			seenFiles[parts[0]] = true
		}
	}

	var out []os.DirEntry
	for name := range seenDirs {
		out = append(out, gitDirEntry{name: name, isDir: true})
	}
	for name := range seenFiles {
		out = append(out, gitDirEntry{name: name, isDir: false})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name() < out[j].Name()
	})

	return out, nil
}

var (
	resolvedPkgNamesMu sync.RWMutex
	resolvedPkgNames   = make(map[string]string)
)

func resolvePackageName(fs archtestFS, importPath string) string {
	if !strings.HasPrefix(importPath, "github.com/matdev83/go-llm-interactive-proxy/") {
		parts := strings.Split(importPath, "/")
		return parts[len(parts)-1]
	}
	if _, isWorkingTree := fs.(*workingTreeFS); isWorkingTree {
		resolvedPkgNamesMu.RLock()
		if name, ok := resolvedPkgNames[importPath]; ok {
			resolvedPkgNamesMu.RUnlock()
			return name
		}
		resolvedPkgNamesMu.RUnlock()
	}
	relDir := strings.TrimPrefix(importPath, "github.com/matdev83/go-llm-interactive-proxy/")
	entries, err := fs.ReadDir(relDir)
	if err != nil {
		parts := strings.Split(importPath, "/")
		return parts[len(parts)-1]
	}
	var resolved string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		content, err := fs.ReadFile(relDir + "/" + entry.Name())
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(string(content), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "package ") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					resolved = parts[1]
					break
				}
			}
		}
		if resolved != "" {
			break
		}
	}
	if resolved == "" {
		parts := strings.Split(importPath, "/")
		resolved = parts[len(parts)-1]
	}
	if _, isWorkingTree := fs.(*workingTreeFS); isWorkingTree {
		resolvedPkgNamesMu.Lock()
		resolvedPkgNames[importPath] = resolved
		resolvedPkgNamesMu.Unlock()
	}
	return resolved
}
