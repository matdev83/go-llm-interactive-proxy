package config

func (c *Config) SecureSessionEffectivelyEnabled() bool {
	if c == nil {
		return false
	}
	ss := &c.SecureSession
	if ss.Enabled == nil {
		return true
	}
	return *ss.Enabled
}

// EffectiveServerAuthMode returns the configured HTTP auth posture. Empty defaults to no_auth
// for developer-local defaults; startup validation restricts no_auth to explicit loopback binds.
func (c *Config) EffectiveServerAuthMode() AuthMode {
	if c == nil {
		return AuthModeNoAuth
	}
	if c.Server.AuthMode == "" {
		return AuthModeNoAuth
	}
	return c.Server.AuthMode
}

// SingleUserLocalMode reports whether startup policy permits local no-auth/synthetic-principal behavior.
func (c *Config) SingleUserLocalMode() bool {
	if c == nil {
		return false
	}
	return c.EffectiveServerAuthMode() == AuthModeNoAuth && IsExplicitLoopbackListenAddress(c.Server.Address)
}
