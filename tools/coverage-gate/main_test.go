package main

import (
	"math/big"
	"testing"
)

func TestParseTotal(t *testing.T) {
	got, err := parseTotal("github.com/example/foo.Func 1 2 3\n total: (statements) 84.2%\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(mustRat("421/5")) != 0 {
		t.Fatalf("got %s, want 84.2", got)
	}
}

func TestParseTotalUsesLastTotalLine(t *testing.T) {
	got, err := parseTotal("total: (statements) 89.9%\ntotal: (statements) 90.0%\n")
	if err != nil || got.Cmp(mustRat("90")) != 0 {
		t.Fatalf("got %v, err %v", got, err)
	}
}

func TestParseTotalRejectsMissingTotal(t *testing.T) {
	if _, err := parseTotal("ok\n"); err == nil {
		t.Fatal("expected missing total error")
	}
}

func TestThresholdComparisonDoesNotUseFloatingPoint(t *testing.T) {
	actual := mustRat("8999999999999999/100000000000000")
	threshold := mustRat("90")
	if actual.Cmp(threshold) >= 0 {
		t.Fatal("value below threshold was accepted")
	}
}

func mustRat(value string) *big.Rat {
	result, ok := new(big.Rat).SetString(value)
	if !ok {
		panic(value)
	}
	return result
}
