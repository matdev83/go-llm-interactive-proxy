package interleavedstate

import (
	"encoding/json"
	"testing"
)

func TestRoleConstants(t *testing.T) {
	t.Parallel()
	if RoleNone != "" {
		t.Fatalf("RoleNone must be empty string, got %q", RoleNone)
	}
	if RoleThinker != "thinker" || RoleExecutor != "executor" {
		t.Fatalf("role constants: thinker=%q executor=%q", RoleThinker, RoleExecutor)
	}
}

func TestCycleState_IsEmpty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		c    CycleState
		want bool
	}{
		{"zero", CycleState{}, true},
		{"empty sequence", CycleState{SelectorKey: "k", Sequence: nil, NextIndex: 0}, true},
		{"with sequence", CycleState{SelectorKey: "k", Sequence: []CycleEntry{{Key: "a", Role: RoleExecutor}}, NextIndex: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.c.IsEmpty(); got != tc.want {
				t.Fatalf("IsEmpty=%v want %v", got, tc.want)
			}
		})
	}
}

func TestCycleState_Validate_CursorBounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		c       CycleState
		wantErr bool
	}{
		{"empty valid", CycleState{}, false},
		{"index zero valid", CycleState{SelectorKey: "k", Sequence: []CycleEntry{{Key: "a", Role: RoleExecutor}}, NextIndex: 0}, false},
		{"index at end-1 valid", CycleState{SelectorKey: "k", Sequence: []CycleEntry{{Key: "a"}, {Key: "b"}}, NextIndex: 1}, false},
		{"index negative invalid", CycleState{SelectorKey: "k", Sequence: []CycleEntry{{Key: "a"}}, NextIndex: -1}, true},
		{"index past end invalid", CycleState{SelectorKey: "k", Sequence: []CycleEntry{{Key: "a"}}, NextIndex: 1}, true},
		{"index equals len invalid", CycleState{SelectorKey: "k", Sequence: []CycleEntry{{Key: "a"}, {Key: "b"}}, NextIndex: 2}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.c.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestCycleState_MatchesSelector_StaleDetection(t *testing.T) {
	t.Parallel()
	c := CycleState{SelectorKey: "selector-v1", Sequence: []CycleEntry{{Key: "a", Role: RoleExecutor}}, NextIndex: 0}
	if !c.MatchesSelector("selector-v1") {
		t.Fatal("expected matching selector to be fresh")
	}
	if c.MatchesSelector("selector-v2") {
		t.Fatal("expected different selector to be stale")
	}
	empty := CycleState{}
	if !empty.MatchesSelector("anything") {
		t.Fatal("empty state should not be considered stale for any selector")
	}
}

func TestMemoRef_IsEmptyAndEqual(t *testing.T) {
	t.Parallel()
	var zeroRef MemoRef
	if !zeroRef.IsEmpty() {
		t.Fatal("zero MemoRef must be empty")
	}
	a := MemoRef{Key: "k1", Version: 3}
	if a.IsEmpty() {
		t.Fatal("non-zero MemoRef must not be empty")
	}
	if !a.Equal(MemoRef{Key: "k1", Version: 3}) {
		t.Fatal("equal MemoRefs must compare equal")
	}
	if a.Equal(MemoRef{Key: "k1", Version: 4}) {
		t.Fatal("different version must not compare equal")
	}
	if a.Equal(MemoRef{Key: "k2", Version: 3}) {
		t.Fatal("different key must not compare equal")
	}
}

func TestState_IsEmptyAndEqual(t *testing.T) {
	t.Parallel()
	var zeroState State
	if !zeroState.IsEmpty() {
		t.Fatal("zero State must be empty")
	}
	s1 := State{
		Cycle:   CycleState{SelectorKey: "k", Sequence: []CycleEntry{{Key: "a", Role: RoleThinker}}, NextIndex: 0},
		MemoRef: &MemoRef{Key: "m1", Version: 1},
	}
	if s1.IsEmpty() {
		t.Fatal("populated State must not be empty")
	}
	if !s1.Equal(s1) {
		t.Fatal("State must equal itself")
	}
	s2 := s1
	s2.MemoRef = &MemoRef{Key: "m1", Version: 2}
	if s1.Equal(s2) {
		t.Fatal("different memo version must not be equal")
	}
	s3 := State{Cycle: s1.Cycle}
	if s1.Equal(s3) {
		t.Fatal("nil vs non-nil memo ref must not be equal")
	}
}

func TestState_Validate(t *testing.T) {
	t.Parallel()
	var empty State
	if err := empty.Validate(); err != nil {
		t.Fatalf("empty state validate: %v", err)
	}
	good := State{
		Cycle:   CycleState{SelectorKey: "k", Sequence: []CycleEntry{{Key: "a"}}, NextIndex: 0},
		MemoRef: &MemoRef{Key: "m1", Version: 1},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("good state validate: %v", err)
	}
	bad := State{Cycle: CycleState{Sequence: []CycleEntry{{Key: "a"}}, NextIndex: 5}}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected validation error for out-of-bounds cursor")
	}
}

func TestCycleState_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	c := CycleState{
		SelectorKey: "selector-v1",
		Sequence: []CycleEntry{
			{Key: "exec-a", Role: RoleExecutor},
			{Key: "thinker", Role: RoleThinker},
		},
		NextIndex: 1,
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CycleState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Equal(c) {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, c)
	}
}

func TestState_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	s := State{
		Cycle:   CycleState{SelectorKey: "k", Sequence: []CycleEntry{{Key: "a", Role: RoleExecutor}}, NextIndex: 0},
		MemoRef: &MemoRef{Key: "m1", Version: 2},
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got State
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Equal(s) {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, s)
	}
}

func TestState_JSONRoundTrip_EmptyOmitsMemoRef(t *testing.T) {
	t.Parallel()
	s := State{Cycle: CycleState{SelectorKey: "k", Sequence: []CycleEntry{{Key: "a"}}, NextIndex: 0}}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got State
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MemoRef != nil {
		t.Fatalf("expected nil memo ref after round-trip, got %+v", got.MemoRef)
	}
	if !got.Equal(s) {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, s)
	}
}

func TestMemoRef_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	r := MemoRef{Key: "abc", Version: 9}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got MemoRef
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Equal(r) {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, r)
	}
}
