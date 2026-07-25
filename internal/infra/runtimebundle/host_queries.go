package runtimebundle

// Ready reports whether the host has a usable active-generation executor.
func (h *ReloadHost) Ready() bool {
	return h != nil && h.Manager != nil && h.Executor != nil && h.Manager.Active() != nil
}
