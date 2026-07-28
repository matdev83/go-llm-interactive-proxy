package credpool

import "errors"

// ErrNoUsableCredential is returned by [Pool.Acquire] when every credential is
// excluded, in cooldown, or permanently auth-invalid. Its message never
// includes API key material.
var ErrNoUsableCredential = errors.New("credpool: no usable credential")

var errEmptyCredentialList = errors.New("credpool: empty credential list")
