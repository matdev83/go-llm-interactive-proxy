package session

// SessionView is a read-only snapshot of session state exposed to plugins (design §2).
type SessionView struct {
	SessionID string
	ALegID    string
	IsNew     bool
	Labels    map[string]string
}
