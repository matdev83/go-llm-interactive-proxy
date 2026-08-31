package testcost

import (
	"errors"
	"strings"
	"testing"
)

func TestParseTestJSONUsesPackagePassAndNeverSumsWall(t *testing.T) {
	measurement, err := ParseTestJSON(strings.NewReader(`{"Action":"pass","Package":"example/pkg","Test":"TestOne","Elapsed":9}
{"Action":"pass","Package":"example/pkg","Elapsed":1.25}
`), TargetTestUnit)
	if err != nil {
		t.Fatalf("ParseTestJSON() error = %v", err)
	}
	if got := measurement.Packages["example/pkg"].ElapsedNanos; got != 1_250_000_000 {
		t.Fatalf("package elapsed = %d, want 1250000000", got)
	}
	if measurement.WallNanos != 0 {
		t.Fatalf("parser summed package elapsed into wall_nanos: %d", measurement.WallNanos)
	}
	if _, err := ParseTestJSON(strings.NewReader(`{"Action":"pass","Package":"example/pkg","Elapsed":1}`)); !errors.Is(err, ErrMissingTarget) {
		t.Fatalf("missing parser target error = %v", err)
	}
}
