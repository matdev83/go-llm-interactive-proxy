package auxiliary

import "errors"

// ErrNotConfigured means no auxiliary client is bound for this execution snapshot.
var ErrNotConfigured = errors.New("lipsdk/auxiliary: auxiliary client not configured")

// ErrAuxDepthExceeded means nested auxiliary calls exceeded the runtime limit.
var ErrAuxDepthExceeded = errors.New("lipsdk/auxiliary: auxiliary recursion depth exceeded")
