package routing

import (
	"math/rand"
	"testing"
)

func TestExpandFailoverLeftToRightPrimaries(t *testing.T) {
	t.Parallel()
	sel, err := Parse("openai:gpt-4|anthropic:opus|bedrock:claude")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("len %d", len(out))
	}
	if out[0].Key != "openai:gpt-4" || out[1].Key != "anthropic:opus" {
		t.Fatalf("order: %#v", out)
	}
}

func TestExpandFailoverSkipsExcludedPrimary(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a:b|c:d")
	if err != nil {
		t.Fatal(err)
	}
	ex := map[string]struct{}{"a:b": {}}
	out, err := ExpandFailover(sel, PlanOptions{Excluded: ex})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "c:d" {
		t.Fatalf("got %#v", out)
	}
}

func TestExpandFailoverSkipsUnhealthyPrimary(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a:b|c:d")
	if err != nil {
		t.Fatal(err)
	}
	uh := map[string]struct{}{"a:b": {}}
	out, err := ExpandFailover(sel, PlanOptions{Unhealthy: uh})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "c:d" {
		t.Fatalf("got %#v", out)
	}
}

func rng(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

func TestWeightedDeterministic(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[weight=1]a:x^[weight=1]b:y")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{Rand: rng(0), Session: &SessionRoutingState{FirstRequestConsumed: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("len %d", len(out))
	}
	// math/rand source(0): first Intn(2)==0 — picks first weighted branch (a:x).
	if out[0].Key != "a:x" {
		t.Fatalf("got %q", out[0].Key)
	}
}

func TestFirstRequestForcesBranch(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]cheap:fast^[weight=100]expensive:slow")
	if err != nil {
		t.Fatal(err)
	}
	sess := &SessionRoutingState{}
	out, err := ExpandFailover(sel, PlanOptions{Rand: rng(99), Session: sess})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "cheap:fast" {
		t.Fatalf("got %#v", out)
	}
	if !sess.FirstRequestConsumed {
		t.Fatal("session should mark first consumed")
	}
}

func TestFirstRequestIgnoredAfterConsumedUsesEligibleSet(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]a:a^[weight=1]b:b")
	if err != nil {
		t.Fatal(err)
	}
	sess := &SessionRoutingState{FirstRequestConsumed: true}
	ex := map[string]struct{}{"a:a": {}}
	out, err := ExpandFailover(sel, PlanOptions{Rand: rng(0), Session: sess, Excluded: ex})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "b:b" {
		t.Fatalf("got %#v", out)
	}
}

func TestReplanWeightedIgnoresFirst(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]cheap:fast^[weight=1]expensive:slow")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	ex := map[string]struct{}{"cheap:fast": {}}
	opt := PlanOptions{
		Rand:        rng(0),
		Session:     &SessionRoutingState{FirstRequestConsumed: false},
		Excluded:    ex,
		IsRetryPath: false,
	}
	c, err := ReplanWeighted(w, opt)
	if err != nil {
		t.Fatal(err)
	}
	if c.Key != "expensive:slow" {
		t.Fatalf("got %q", c.Key)
	}
}

func TestWeightedSkipsToNextFailoverWhenAllExcluded(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[weight=1]a:b^[weight=1]c:d|fallback:model")
	if err != nil {
		t.Fatal(err)
	}
	ex := map[string]struct{}{"a:b": {}, "c:d": {}}
	out, err := ExpandFailover(sel, PlanOptions{Rand: rng(0), Excluded: ex})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "fallback:model" {
		t.Fatalf("got %#v", out)
	}
}
