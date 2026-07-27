package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
)

// canonicalModuleFile normalizes recognized text line endings (CRLF/CR → LF)
// so go.mod / go.sum comparisons are portable across Windows checkouts.
func canonicalModuleFile(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	return b
}

// moduleFilesDrift reports semantic drift between current and tidy-produced
// module file bytes after line-ending canonicalization.
func moduleFilesDrift(name string, current, tidied []byte) error {
	if bytes.Equal(canonicalModuleFile(current), canonicalModuleFile(tidied)) {
		return nil
	}
	return fmt.Errorf("%s differs after go mod tidy", name)
}

// tidyDiffOnlyLineEndings reports whether a failed `go mod tidy -diff` output
// is solely CRLF↔LF rewriting of go.mod / go.sum (same logical lines).
// Non-diff failures (for example checksum security errors) return false.
func tidyDiffOnlyLineEndings(diff []byte) bool {
	if !bytes.Contains(diff, []byte("diff current/")) {
		return false
	}
	var minus, plus [][]byte
	flush := func() bool {
		if len(minus) != len(plus) {
			return false
		}
		for i := range minus {
			if !bytes.Equal(minus[i], plus[i]) {
				return false
			}
		}
		minus, plus = nil, nil
		return true
	}
	sawFile := false
	for _, line := range bytes.Split(diff, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch {
		case bytes.HasPrefix(line, []byte("diff ")):
			if sawFile && !flush() {
				return false
			}
			sawFile = true
		case bytes.HasPrefix(line, []byte("--- ")),
			bytes.HasPrefix(line, []byte("+++ ")),
			bytes.HasPrefix(line, []byte("@@")):
			// headers / hunk marks
		case bytes.HasPrefix(line, []byte("-")):
			minus = append(minus, canonicalModuleFile(line[1:]))
		case bytes.HasPrefix(line, []byte("+")):
			plus = append(plus, canonicalModuleFile(line[1:]))
		case bytes.HasPrefix(line, []byte("\\")):
			// "\ No newline at end of file" — ignore
		case len(bytes.TrimSpace(line)) == 0:
			// blank
		default:
			// unexpected non-diff diagnostics mixed in
			return false
		}
	}
	if !sawFile {
		return false
	}
	return flush()
}

// checkModuleTidy runs `go mod tidy -diff` in modRoot (so local replace
// directives keep resolving) and treats CRLF↔LF-only rewrites as tidy-clean.
// go mod tidy -diff restores module files itself; this gate does not leave
// lasting worktree mutations.
func checkModuleTidy(modRoot string) ([]byte, error) {
	cmd := exec.Command("go", "mod", "tidy", "-diff")
	cmd.Dir = modRoot
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return out, nil
	}
	if tidyDiffOnlyLineEndings(out) {
		return out, nil
	}
	return out, fmt.Errorf("go mod tidy -diff: %w", err)
}
