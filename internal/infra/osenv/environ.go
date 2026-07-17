package osenv

import (
	"os"
	"slices"
)

// Process implements secretsguard.Environment using the process environment.
type Process struct{}

// Lookup implements secretsguard.Environment.
func (Process) Lookup(name string) (string, bool) {
	return os.LookupEnv(name)
}

// Snapshot implements secretsguard.Environment.
func (Process) Snapshot() []string {
	return slices.Clone(os.Environ())
}
