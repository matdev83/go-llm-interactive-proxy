package testcost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"runtime"
)

type TestEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
}

// ParseTestJSON extracts package-level pass events. It intentionally does not
// derive process wall time from package elapsed values.
func ParseTestJSON(input io.Reader, target ...string) (Measurement, error) {
	if len(target) != 1 || target[0] == "" {
		return Measurement{}, ErrMissingTarget
	}
	measurement := Measurement{
		SchemaVersion: SchemaVersion,
		Target:        target[0],
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		GoVersion:     runtime.Version(),
		LogicalCPUs:   runtime.NumCPU(),
		Packages:      make(map[string]PackageMetrics),
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		data := bytes.TrimSpace(scanner.Bytes())
		if len(data) == 0 {
			continue
		}
		if data[0] != '{' {
			return Measurement{}, fmt.Errorf("%w at line %d: event must be an object", ErrMalformedJSON, line)
		}
		var event TestEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return Measurement{}, fmt.Errorf("%w at line %d: %v", ErrMalformedJSON, line, err)
		}
		if event.Action != "pass" || event.Test != "" {
			continue
		}
		if event.Package == "" {
			return Measurement{}, fmt.Errorf("%w at line %d: package pass has no package", ErrMalformedJSON, line)
		}
		if math.IsNaN(event.Elapsed) || math.IsInf(event.Elapsed, 0) || event.Elapsed < 0 {
			return Measurement{}, fmt.Errorf("%w at line %d: invalid elapsed", ErrMalformedJSON, line)
		}
		measurement.Packages[event.Package] = PackageMetrics{ElapsedNanos: secondsToNanosFloat(event.Elapsed)}
	}
	if err := scanner.Err(); err != nil {
		return Measurement{}, fmt.Errorf("%w: read test JSON: %v", ErrMalformedJSON, err)
	}
	return measurement, nil
}

func ParseTest2JSON(input io.Reader, target ...string) (Measurement, error) {
	return ParseTestJSON(input, target...)
}
