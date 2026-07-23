package archtest

import "fmt"

// Convergence gate identifiers for Task 1.4 RED architecture gates.
// Task 3.5 adds http/generation contraction gates (broad RequestPlane deletion,
// focused HTTP lifecycle, Phase 4 Built grandfathering).
const (
	gateRuntimeConvergence = "runtime_convergence"
	gateReloadContract     = "reload_contract"
	gateHostPath           = "host_path"
	gateConfigLoad         = "config_load"
)

// Allowed classifications for migration allowlist entries.
const (
	classDeclaration = "declaration"
	classCall        = "call"
	classAdapter     = "adapter"
	classOwner       = "owner"
)

var knownConvergenceGates = map[string]bool{
	gateRuntimeConvergence:   true,
	gateReloadContract:       true,
	gateHostPath:             true,
	gateConfigLoad:           true,
	gateBroadRequestPlane:    true,
	gateCompatHTTPSymbols:    true,
	gateFocusedHTTPLifecycle: true,
	gateStdhttpBuilt:           true,
	gateCanonicalClosers:       true,
	gateCandidateLegacyClosers: true,
	gateComposeInventory:       true,
}

var knownConvergenceClasses = map[string]bool{
	classDeclaration: true,
	classCall:        true,
	classAdapter:     true,
	classOwner:       true,
}

// convergenceFinding is one production (or fixture) hit for a convergence gate.
type convergenceFinding struct {
	Gate           string
	Path           string
	Identity       string
	Classification string
	Detail         string
}

func (f convergenceFinding) key() string {
	return f.Gate + "|" + f.Path + "|" + f.Identity
}

func (f convergenceFinding) String() string {
	return fmt.Sprintf("%s %s %s (%s): %s", f.Gate, f.Path, f.Identity, f.Classification, f.Detail)
}

// convergenceAllowlistEntry is one explicit migration exception.
type convergenceAllowlistEntry struct {
	Gate           string `json:"gate"`
	Path           string `json:"path"`
	Identity       string `json:"identity"`
	Classification string `json:"classification"`
	RetirementTask string `json:"retirement_task"`
	Rationale      string `json:"rationale"`
}

func (e convergenceAllowlistEntry) key() string {
	return e.Gate + "|" + e.Path + "|" + e.Identity
}

type convergenceAllowlistFile struct {
	SchemaVersion int                         `json:"schema_version"`
	Description   string                      `json:"description"`
	Entries       []convergenceAllowlistEntry `json:"entries"`
}
