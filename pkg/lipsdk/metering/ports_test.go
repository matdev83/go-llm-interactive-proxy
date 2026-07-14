package metering_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type fakeRecorder struct {
	facts []metering.Fact
}

func (f *fakeRecorder) Append(ctx context.Context, fact metering.Fact) error {
	_ = ctx
	f.facts = append(f.facts, fact)
	return nil
}

type fakeQuery struct {
	page metering.Page
}

func (f fakeQuery) List(ctx context.Context, q metering.Query) (metering.Page, error) {
	_ = ctx
	_ = q
	return f.page, nil
}

func TestRecorderAndQueryCompileWithFakes(t *testing.T) {
	t.Parallel()
	var rec metering.Recorder = &fakeRecorder{}
	var qry metering.Querier = fakeQuery{page: metering.Page{Facts: nil, NextCursor: ""}}
	if err := rec.Append(context.Background(), metering.Fact{FactID: "f"}); err != nil {
		t.Fatal(err)
	}
	page, err := qry.List(context.Background(), metering.Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != "" {
		t.Fatalf("unexpected cursor %q", page.NextCursor)
	}
}
