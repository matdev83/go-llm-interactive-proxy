package execerr_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestClassifyExecute_reject(t *testing.T) {
	t.Parallel()
	err := &lipapi.RejectError{Reason: "missing thing"}
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.ClientReject {
		t.Fatalf("kind: %v", out.Kind)
	}
	if out.Status != http.StatusBadRequest {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != "missing thing" {
		t.Fatalf("message: %q", out.Message)
	}
	if out.Err != err {
		t.Fatalf("Err: want same reject pointer")
	}
}

func TestClassifyExecute_internal(t *testing.T) {
	t.Parallel()
	err := errors.New("backend unavailable")
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.InternalError {
		t.Fatalf("kind: %v", out.Kind)
	}
	if out.Status != http.StatusInternalServerError {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != execerr.InternalWireMessage {
		t.Fatalf("message: %q (want non-revealing wire text)", out.Message)
	}
	if out.Err != err {
		t.Fatalf("Err: want original for logging")
	}
}

func TestClassifyExecute_nil(t *testing.T) {
	t.Parallel()
	out := execerr.ClassifyExecute(nil)
	if out.Kind != execerr.InternalError {
		t.Fatalf("kind: %v", out.Kind)
	}
	if out.Status != http.StatusInternalServerError {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != "unknown error" {
		t.Fatalf("message: %q", out.Message)
	}
	if out.Err != nil {
		t.Fatalf("Err: %v", out.Err)
	}
}
