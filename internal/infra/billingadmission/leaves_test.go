package billingadmission

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

func TestCollectPlannedLeavesCoversFailoverWeightedAndParallel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		selector string
		want     []plannedLeaf
	}{
		{
			name:     "ordered failover",
			selector: "backend:cheap|backend:expensive",
			want: []plannedLeaf{
				{Key: "backend:cheap", Backend: "backend", Model: "cheap"},
				{Key: "backend:expensive", Backend: "backend", Model: "expensive"},
			},
		},
		{
			name:     "parallel race",
			selector: "a:m1!b:m2!c:m3",
			want: []plannedLeaf{
				{Key: "a:m1", Backend: "a", Model: "m1"},
				{Key: "b:m2", Backend: "b", Model: "m2"},
				{Key: "c:m3", Backend: "c", Model: "m3"},
			},
		},
		{
			name:     "weighted group",
			selector: "[weight=1]a:x^[weight=1]b:y",
			want: []plannedLeaf{
				{Key: "a:x", Backend: "a", Model: "x"},
				{Key: "b:y", Backend: "b", Model: "y"},
			},
		},
		{
			name:     "failover duplicate leaves retained",
			selector: "backend:model|backend:model",
			want: []plannedLeaf{
				{Key: "backend:model", Backend: "backend", Model: "model"},
				{Key: "backend:model", Backend: "backend", Model: "model"},
			},
		},
		{
			name:     "parallel duplicate leaves retained",
			selector: "a:m1!a:m1",
			want: []plannedLeaf{
				{Key: "a:m1", Backend: "a", Model: "m1"},
				{Key: "a:m1", Backend: "a", Model: "m1"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sel, err := routing.Parse(tt.selector)
			if err != nil {
				t.Fatal(err)
			}
			got := collectPlannedLeaves(sel)
			if len(got) != len(tt.want) {
				t.Fatalf("leaves = %+v, want %+v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("leaf[%d] = %+v, want %+v (all=%+v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
	if collectPlannedLeaves(nil) != nil {
		t.Fatal("nil selector must yield no leaves")
	}

	nested := &routing.Selector{Alternatives: []routing.FailoverAlt{{
		Weighted: &routing.Weighted{Branches: []routing.WeightedBranch{{
			Weight: 1,
			Parallel: &routing.Parallel{Branches: []routing.ParallelBranch{
				{Target: routing.Primary{Backend: "a", Model: "m1"}},
				{Target: routing.Primary{Backend: "b", Model: "m2"}},
			}},
		}}},
	}}}
	got := collectPlannedLeaves(nested)
	if len(got) != 2 || got[0].Key != "a:m1" || got[1].Key != "b:m2" {
		t.Fatalf("nested weighted parallel leaves = %+v", got)
	}
}
