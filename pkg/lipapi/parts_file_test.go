package lipapi_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestPartFileRef_requiresFileRefThroughCall(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind: lipapi.PartFileRef,
			}},
		}},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for empty FileRef")
	}
}

func TestPartFileRef_validThroughCall(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.FilePart("file-abc123", "application/pdf", "report.pdf"),
			},
		}},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPartFileRef_emptyFileRefRejected(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind:     lipapi.PartFileRef,
				FileMIME: "application/pdf",
			}},
		}},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for empty FileRef")
	}
}

func TestPartFileRef_mimeAndNameOptional(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind:    lipapi.PartFileRef,
				FileRef: "file-xyz",
			}},
		}},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
