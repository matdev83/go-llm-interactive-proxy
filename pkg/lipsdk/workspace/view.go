package workspace

// WorkspaceView is a read-only workspace snapshot for safety and path policy plugins (design §2).
type WorkspaceView struct {
	ProjectRoot string
	DirtyTree   bool
	Markers     []string
	Labels      map[string]string
}
