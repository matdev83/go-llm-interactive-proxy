package lipapi_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// harnessAlias is one surveyed coding-agent tool name and its expected
// classification. harness is provenance only: the classifier matches by name
// (trim + case-fold + exact alias), never by provider/harness identity.
type harnessAlias struct {
	harness   string
	toolName  string
	category  lipapi.ToolCategory
	mayMutate bool
}

// harnessAliasMatrix is the single data source for the cross-harness tool-name
// survey (`.kiro/specs/tool-call-classification/research.md`): Codex, Pi, Cline,
// OpenCode, Hermes, OpenClaw, Kilo Code, and Claude Code. Case-folding covers
// casing dialects (Claude Code's PascalCase `Read`/`Bash` fold to the same exact
// aliases as snake_case `read`/`bash`), so no per-harness production branch exists.
//
// Multi-word camelCase names are intentionally NOT normalized to snake_case: the
// classifier is exact-name after case-folding, so `NotebookRead` folds to
// "notebookread" and falls back to unknown/true while `notebook_read` (the surveyed
// snake_case alias) classifies as file_read.
var harnessAliasMatrix = []harnessAlias{
	// Claude Code — PascalCase single-word dialects.
	{"claude", "Read", lipapi.ToolCategoryFileRead, false},
	{"claude", "Grep", lipapi.ToolCategoryFileSearch, false},
	{"claude", "Glob", lipapi.ToolCategoryFileSearch, false},
	{"claude", "LS", lipapi.ToolCategoryFileSearch, false},
	{"claude", "Bash", lipapi.ToolCategoryOSCommand, true},
	{"claude", "Edit", lipapi.ToolCategoryFileEdit, true},
	{"claude", "Write", lipapi.ToolCategoryFileEdit, true},
	{"claude", "WebFetch", lipapi.ToolCategoryWebAccess, false},
	{"claude", "WebSearch", lipapi.ToolCategoryWebAccess, false},
	// Concatenated PascalCase is not a case variant of notebook_read (no underscore).
	{"claude", "NotebookRead", lipapi.ToolCategoryUnknown, true},

	// OpenAI Codex.
	{"codex", "read_file", lipapi.ToolCategoryFileRead, false},
	{"codex", "write_file", lipapi.ToolCategoryFileEdit, true},
	{"codex", "edit", lipapi.ToolCategoryFileEdit, true},
	{"codex", "apply_patch", lipapi.ToolCategoryFileEdit, true},
	{"codex", "grep", lipapi.ToolCategoryFileSearch, false},
	{"codex", "glob", lipapi.ToolCategoryFileSearch, false},
	{"codex", "web_search", lipapi.ToolCategoryWebAccess, false},
	{"codex", "notebook_read", lipapi.ToolCategoryFileRead, false},
	{"codex", "notebook_edit", lipapi.ToolCategoryFileEdit, true},

	// Pi.
	{"pi", "read", lipapi.ToolCategoryFileRead, false},
	{"pi", "write", lipapi.ToolCategoryFileEdit, true},
	{"pi", "edit", lipapi.ToolCategoryFileEdit, true},
	{"pi", "bash", lipapi.ToolCategoryOSCommand, true},
	{"pi", "grep", lipapi.ToolCategoryFileSearch, false},
	{"pi", "glob", lipapi.ToolCategoryFileSearch, false},
	{"pi", "ls", lipapi.ToolCategoryFileSearch, false},
	{"pi", "find", lipapi.ToolCategoryFileSearch, false},
	{"pi", "list_files", lipapi.ToolCategoryFileSearch, false},
	{"pi", "semantic_search", lipapi.ToolCategoryFileSearch, false},
	{"pi", "codebase_search", lipapi.ToolCategoryFileSearch, false},
	{"pi", "web_search", lipapi.ToolCategoryWebAccess, false},
	{"pi", "web_fetch", lipapi.ToolCategoryWebAccess, false},
	{"pi", "notebook_read", lipapi.ToolCategoryFileRead, false},
	{"pi", "notebook_edit", lipapi.ToolCategoryFileEdit, true},
	{"pi", "notebookedit", lipapi.ToolCategoryFileEdit, true},
	{"pi", "execute_command", lipapi.ToolCategoryOSCommand, true},
	{"pi", "exec", lipapi.ToolCategoryOSCommand, true},
	{"pi", "exec_command", lipapi.ToolCategoryOSCommand, true},
	{"pi", "terminal", lipapi.ToolCategoryOSCommand, true},
	{"pi", "process", lipapi.ToolCategoryOSCommand, true},
	{"pi", "write_stdin", lipapi.ToolCategoryOSCommand, true},
	{"pi", "background_process", lipapi.ToolCategoryOSCommand, true},
	{"pi", "interactive_terminal", lipapi.ToolCategoryOSCommand, true},
	{"pi", "powershell", lipapi.ToolCategoryOSCommand, true},

	// Cline.
	{"cline", "execute_command", lipapi.ToolCategoryOSCommand, true},
	{"cline", "read_file", lipapi.ToolCategoryFileRead, false},
	{"cline", "write_to_file", lipapi.ToolCategoryFileEdit, true},
	{"cline", "replace_in_file", lipapi.ToolCategoryFileEdit, true},
	{"cline", "search_files", lipapi.ToolCategoryFileSearch, false},
	{"cline", "list_files", lipapi.ToolCategoryFileSearch, false},
	{"cline", "list_code_definition_names", lipapi.ToolCategoryFileSearch, false},
	{"cline", "web_fetch", lipapi.ToolCategoryWebAccess, false},
	{"cline", "browser_action", lipapi.ToolCategoryWebAccess, true},

	// Kilo Code (Cline fork — same tool surface).
	{"kilo", "execute_command", lipapi.ToolCategoryOSCommand, true},
	{"kilo", "read_file", lipapi.ToolCategoryFileRead, false},
	{"kilo", "write_to_file", lipapi.ToolCategoryFileEdit, true},
	{"kilo", "replace_in_file", lipapi.ToolCategoryFileEdit, true},
	{"kilo", "search_files", lipapi.ToolCategoryFileSearch, false},
	{"kilo", "list_files", lipapi.ToolCategoryFileSearch, false},
	{"kilo", "list_code_definition_names", lipapi.ToolCategoryFileSearch, false},
	{"kilo", "web_fetch", lipapi.ToolCategoryWebAccess, false},
	{"kilo", "browser_action", lipapi.ToolCategoryWebAccess, true},

	// OpenCode.
	{"opencode", "read", lipapi.ToolCategoryFileRead, false},
	{"opencode", "write", lipapi.ToolCategoryFileEdit, true},
	{"opencode", "edit", lipapi.ToolCategoryFileEdit, true},
	{"opencode", "patch", lipapi.ToolCategoryFileEdit, true},
	{"opencode", "bash", lipapi.ToolCategoryOSCommand, true},
	{"opencode", "grep", lipapi.ToolCategoryFileSearch, false},
	{"opencode", "glob", lipapi.ToolCategoryFileSearch, false},
	{"opencode", "ls", lipapi.ToolCategoryFileSearch, false},
	{"opencode", "search_files", lipapi.ToolCategoryFileSearch, false},
	{"opencode", "list_files", lipapi.ToolCategoryFileSearch, false},
	{"opencode", "semantic_search", lipapi.ToolCategoryFileSearch, false},
	{"opencode", "codebase_search", lipapi.ToolCategoryFileSearch, false},
	{"opencode", "web_search", lipapi.ToolCategoryWebAccess, false},
	{"opencode", "web_fetch", lipapi.ToolCategoryWebAccess, false},
	{"opencode", "web_extract", lipapi.ToolCategoryWebAccess, false},
	{"opencode", "x_search", lipapi.ToolCategoryWebAccess, false},
	{"opencode", "terminal", lipapi.ToolCategoryOSCommand, true},
	{"opencode", "process", lipapi.ToolCategoryOSCommand, true},

	// OpenClaw.
	{"openclaw", "read", lipapi.ToolCategoryFileRead, false},
	{"openclaw", "write", lipapi.ToolCategoryFileEdit, true},
	{"openclaw", "edit", lipapi.ToolCategoryFileEdit, true},
	{"openclaw", "apply_patch", lipapi.ToolCategoryFileEdit, true},
	{"openclaw", "bash", lipapi.ToolCategoryOSCommand, true},
	{"openclaw", "grep", lipapi.ToolCategoryFileSearch, false},
	{"openclaw", "glob", lipapi.ToolCategoryFileSearch, false},
	{"openclaw", "search_files", lipapi.ToolCategoryFileSearch, false},
	{"openclaw", "list_files", lipapi.ToolCategoryFileSearch, false},
	{"openclaw", "web_search", lipapi.ToolCategoryWebAccess, false},
	{"openclaw", "web_fetch", lipapi.ToolCategoryWebAccess, false},
	{"openclaw", "browser_action", lipapi.ToolCategoryWebAccess, true},
	{"openclaw", "execute_command", lipapi.ToolCategoryOSCommand, true},

	// Hermes Agent (file, shell, and browser toolset).
	{"hermes", "read_file", lipapi.ToolCategoryFileRead, false},
	{"hermes", "write_file", lipapi.ToolCategoryFileEdit, true},
	{"hermes", "edit", lipapi.ToolCategoryFileEdit, true},
	{"hermes", "grep", lipapi.ToolCategoryFileSearch, false},
	{"hermes", "shell_command", lipapi.ToolCategoryOSCommand, true},
	{"hermes", "web.run", lipapi.ToolCategoryWebAccess, false},
	{"hermes", "web_search", lipapi.ToolCategoryWebAccess, false},
	{"hermes", "websearch", lipapi.ToolCategoryWebAccess, false},
	{"hermes", "web_fetch", lipapi.ToolCategoryWebAccess, false},
	{"hermes", "webfetch", lipapi.ToolCategoryWebAccess, false},
	{"hermes", "web_extract", lipapi.ToolCategoryWebAccess, false},
	{"hermes", "browser", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_action", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_back", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_cdp", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_click", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_console", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_dialog", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_get_images", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_navigate", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_press", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_scroll", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_snapshot", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_type", lipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_vision", lipapi.ToolCategoryWebAccess, true},

	// Explicit removal-only names (generic/compatible, not a surveyed harness
	// primitive — gap-analysis G-12).
	{"generic", "delete_file", lipapi.ToolCategoryFileRemove, true},
	{"generic", "remove_file", lipapi.ToolCategoryFileRemove, true},
	{"generic", "delete_directory", lipapi.ToolCategoryFileRemove, true},
	{"generic", "remove_directory", lipapi.ToolCategoryFileRemove, true},
}

// TestHarnessToolAliasMatrix verifies every surveyed alias in the single
// cross-harness matrix classifies to its expected category and mutation posture.
func TestHarnessToolAliasMatrix(t *testing.T) {
	t.Parallel()

	if len(harnessAliasMatrix) == 0 {
		t.Fatal("harness alias matrix is empty")
	}
	seen := make(map[string]struct{}, len(harnessAliasMatrix))
	for _, a := range harnessAliasMatrix {
		if a.toolName == "" {
			t.Fatalf("matrix row has empty toolName: %#v", a)
		}
		// Rows must be unique per (harness, toolName): a shared alias like `read`
		// legitimately repeats across harnesses, but no single harness may list
		// the same name twice.
		key := a.harness + "\x00" + a.toolName
		if _, dup := seen[key]; dup {
			t.Fatalf("duplicate matrix row %q / %q", a.harness, a.toolName)
		}
		seen[key] = struct{}{}

		gotCat, gotMutate := lipapi.ClassifyToolName(a.toolName)
		if gotCat != a.category || gotMutate != a.mayMutate {
			t.Errorf("%s %q: ClassifyToolName = (%q, %v), want (%q, %v)",
				a.harness, a.toolName, gotCat, gotMutate, a.category, a.mayMutate)
		}
	}
}
