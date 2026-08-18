package auxiliary_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

func TestBackgroundContractIsAdditive(t *testing.T) {
	t.Parallel()
	var _ auxiliary.BackgroundClient = auxiliary.DisabledBackgroundClient{}
	if got := auxiliary.JobID("job-1"); got == "" {
		t.Fatal("JobID must be usable as a typed identifier")
	}
	_ = auxiliary.SubmitOptions{CoalesceKey: "committed-transaction", Timeout: time.Second}
}

func TestDisabledBackgroundClient(t *testing.T) {
	t.Parallel()
	c := auxiliary.DisabledBackgroundClient{}
	id, err := c.SubmitCollect(context.Background(), auxiliary.Request{Call: &lipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "k"})
	if id != "" || err != auxiliary.ErrNotConfigured {
		t.Fatalf("SubmitCollect=%q, %v; want not configured", id, err)
	}
	_, err = c.Await(context.Background(), "job")
	if err != auxiliary.ErrNotConfigured {
		t.Fatalf("Await=%v; want not configured", err)
	}
	c.Forget("job")
}
