package archtest

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// hasStagedRuntimePathChanges reports whether the index contains a staged
// addition, removal, or rename touching internal/core/runtime production Go
// files.
//
// The HEAD-versus-working-tree invariant asserted by
// TestLoadTurnRecvASTFilesAtRef_Contract only holds when HEAD already contains
// every file under analysis. While a commit that adds or removes such a file is
// in progress the two sets necessarily differ, and the pre-commit hook runs
// before HEAD moves. Detecting that state lets the comparison be skipped
// locally while CI still enforces it at full strength on the committed tree.
//
// `git diff --cached --name-status` is used rather than `--quiet` because the
// caller needs the affected paths, not only a boolean.
func hasStagedRuntimePathChanges(ctx context.Context, root string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "diff", "--cached", "--name-status", "--", "internal/core/runtime")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, fmt.Errorf("git diff --cached failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// Status letters that change which paths exist: A(dd), D(elete),
		// R(enamed). A modification keeps the path set identical.
		status := fields[0]
		if strings.ContainsAny(status, "ADR") {
			return true, nil
		}
	}
	return false, nil
}
