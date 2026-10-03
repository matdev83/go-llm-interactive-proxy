package archtest

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// hasRuntimePathSetChanges reports whether the set of internal/core/runtime
// production Go files differs between HEAD and what a commit would publish.
//
// The HEAD-versus-working-tree invariant asserted by
// TestLoadTurnRecvASTFilesAtRef_Contract only holds when HEAD already contains
// every file under analysis. While a commit that adds or removes such a file is
// in progress the two sets necessarily differ, and the pre-commit hook runs
// before HEAD moves, so asserting equality there would make the introducing
// commit permanently uncommittable.
//
// `git status --porcelain` is used instead of `git diff --cached` because it
// also reports untracked files. A newly authored runtime file is untracked
// until it is staged, and during that window it is already on disk and already
// counted by the working-tree scan.
//
// Status codes that change which paths exist: A(dd), D(elete), R(enamed), and
// ?(untracked). A modification (M) leaves the path set identical, so it is not
// treated as a set change.
func hasRuntimePathSetChanges(ctx context.Context, root string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain", "--", "internal/core/runtime")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, fmt.Errorf("git status --porcelain failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if len(strings.TrimSpace(line)) < 2 {
			continue
		}
		// Porcelain v1 is "XY <path>"; X is the index status, Y the worktree one.
		status := line[:2]
		if strings.ContainsAny(status, "ADR?") {
			return true, nil
		}
	}
	return false, nil
}
