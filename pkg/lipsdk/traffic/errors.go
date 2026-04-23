package traffic

import "errors"

// ErrNotConfigured means no traffic services are bound for this execution snapshot.
var ErrNotConfigured = errors.New("lipsdk/traffic: traffic facade not configured")
