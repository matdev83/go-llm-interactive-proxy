package httpauth

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"

// Transport-local aliases for auth error rendering contracts defined on [github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk]
// so stdhttp and plugins can refer to this subpackage without duplicating types, while
// internal/core avoids a transitive dependency on pkg/lipsdk/transport (see internal/archtest).
type (
	AuthErrorRenderInput  = lipsdk.AuthErrorRenderInput
	AuthErrorRenderResult = lipsdk.AuthErrorRenderResult
	AuthErrorRenderer     = lipsdk.AuthErrorRenderer
)
