package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/gitscope"
)

type SourceState struct {
	Head        string `json:"head"`
	Fingerprint string `json:"fingerprint"`
}

// snapshot identifies HEAD plus both diffs and untracked bytes. It is read-only
// and honors Git's ignored-file boundary; keep handoff artifacts outside the repo.
func snapshot(ctx context.Context, repo string) (SourceState, error) {
	return snapshotWithIndex(ctx, repo, "")
}

func snapshotWithIndex(ctx context.Context, repo, index string) (SourceState, error) {
	git := func(args ...string) ([]byte, error) { return sourceGitWithIndex(ctx, repo, index, args...) }
	head, err := git("rev-parse", "--verify", "HEAD")
	if err != nil {
		return SourceState{}, err
	}
	state := SourceState{Head: strings.TrimSpace(string(head))}
	digest := sha256.New()
	hashPart(digest, []byte(state.Head))
	for _, args := range [][]string{
		{"diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv", "--"},
		{"diff", "--binary", "--no-ext-diff", "--no-textconv", "--"},
	} {
		data, err := git(args...)
		if err != nil {
			return SourceState{}, err
		}
		hashPart(digest, data)
	}
	data, err := git("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return SourceState{}, err
	}
	names := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	slices.Sort(names)
	for _, name := range names {
		if name == "" {
			continue
		}
		hashPart(digest, []byte(name))
		file, err := os.Open(filepath.Join(repo, filepath.FromSlash(name)))
		if err != nil {
			return SourceState{}, err
		}
		content := sha256.New()
		_, copyErr := io.Copy(content, file)
		closeErr := file.Close()
		if copyErr != nil {
			return SourceState{}, copyErr
		}
		if closeErr != nil {
			return SourceState{}, closeErr
		}
		hashPart(digest, content.Sum(nil))
	}
	state.Fingerprint = fmt.Sprintf("%x", digest.Sum(nil))
	return state, nil
}

func hashPart(digest hash.Hash, data []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write(data)
}

func sourceGit(ctx context.Context, repo string, args ...string) ([]byte, error) {
	return sourceGitWithIndex(ctx, repo, "", args...)
}

func sourceGitWithIndex(ctx context.Context, repo, index string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	cmd.Env = gitscope.Environ()
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	data, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("git %v: %w: %s", args, err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("git %v: %w", args, err)
	}
	return data, nil
}
