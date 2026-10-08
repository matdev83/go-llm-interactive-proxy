package main

import (
	"context"
	"io"
	"os"
	"regexp"
	"testing"
)

func TestContracts_MissingTestsCannotPass(t *testing.T) {
	t.Parallel()
	names := []string{"TestOne", "TestTwo"}
	if err := validateListedContracts(names, "TestOne\nTestTwo\nok example 0.1s\n"); err != nil {
		t.Fatal(err)
	}
	if err := validateListedContracts(names, "TestOne\nok example\n"); err == nil {
		t.Fatal("missing test reported as checked")
	}
	pattern := regexp.MustCompile(contractPattern(names))
	if !pattern.MatchString("TestOne") || pattern.MatchString("TestOneOther") {
		t.Fatalf("filter does not select exact contracts: %s", pattern)
	}
}

func TestContracts_ChildFailureCannotPass(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVCHECK_TEST_HELPER", "fail")
	for _, test := range []bool{false, true} {
		if err := runContractCommand(context.Background(), ".", []string{exe, "-test.run=^TestDevcheckHelperProcess$"}, test, io.Discard, io.Discard); err == nil {
			t.Fatalf("failed contract/lint reported as success (test=%v)", test)
		}
	}
}
