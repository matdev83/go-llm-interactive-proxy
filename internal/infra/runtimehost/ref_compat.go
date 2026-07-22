package runtimehost

// Compatibility aliases for task-1.4 reference names.
// Production types (Generation, Lease, Pin, Manager) are authoritative.

// RefGeneration is an alias of Generation.
type RefGeneration = Generation

// RefLease is an alias of Lease.
type RefLease = Lease

// RefPin is an alias of Pin.
type RefPin = Pin

// RefGenerationManager is an alias of Manager.
type RefGenerationManager = Manager

// NewRefGenerationManager constructs a Manager under the task-1.4 name.
func NewRefGenerationManager(maxRetained int, clock *ManualClock) *Manager {
	return NewManager(maxRetained, clock)
}

// PrepareCandidate is the task-1.4 name for Prepare.
func (m *Manager) PrepareCandidate(label string) *Generation {
	return m.Prepare(label)
}
