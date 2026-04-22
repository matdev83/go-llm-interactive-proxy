package diag

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// HeaderDiagnosticsSecret is the HTTP header clients must send when diagnostics.shared_secret is set.
const HeaderDiagnosticsSecret = "X-LIP-Diagnostics-Secret"

// WrapDiagnosticsProtect returns next unchanged when secret is empty; otherwise it requires an exact
// match of HeaderDiagnosticsSecret (constant-time when lengths match).
func WrapDiagnosticsProtect(secret string, next http.Handler) http.Handler {
	want := strings.TrimSpace(secret)
	if want == "" || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimSpace(r.Header.Get(HeaderDiagnosticsSecret))
		if !constantTimeEqualString(got, want) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func constantTimeEqualString(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
