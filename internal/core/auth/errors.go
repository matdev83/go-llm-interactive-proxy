package auth

import "errors"

// ErrDuplicateLocalAPIKeyID is returned when two records share the same key_id.
var ErrDuplicateLocalAPIKeyID = errors.New("auth.local_api_keys: duplicate key_id")
