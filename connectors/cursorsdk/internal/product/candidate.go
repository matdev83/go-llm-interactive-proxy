package product

// Primary names the backend kind and model identity for one attempt.
// External connectors only need Model; Backend is retained for service Resolve/Execute wiring.
type Primary struct {
	Backend string
	Model   string
}

// AttemptCandidate is the connector-local route identity passed to Open/ResolveCaps.
// It deliberately mirrors the fields this connector consumes from the former root
// routing type without importing root-private packages.
type AttemptCandidate struct {
	Primary Primary
	Key     string
}
