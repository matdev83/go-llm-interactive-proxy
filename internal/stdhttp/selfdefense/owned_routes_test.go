package selfdefense

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/ingressdefense"
)

// clientAddr is the source address the owned-route cases use, so the carve
// assertions and the probe assertions can be compared on one identity.
var clientAddr = netip.MustParseAddr("203.0.113.10")

// TestFrozenFamiliesClaimOperatorConfiguredBasePaths is the matcher-level
// statement of the defect this file exists for. OpenResponses accepts any
// normalized absolute non-root base_path, so /wp-admin and /.github are valid
// operator configurations that produce real data-plane routes, and the frozen
// families claim them. Without an owned-route carve the gate answers those routes
// with the generic 404 before the frontend ever sees them.
//
// This test is deliberately an assertion that the families DO collide: it is the
// evidence that the carve is load-bearing rather than theoretical, and it fails
// loudly if someone later narrows the families to where the carve would be dead
// code.
func TestFrozenFamiliesClaimOperatorConfiguredBasePaths(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/wp-admin/responses",
		"/wp-admin/responses/compact",
		"/.github/responses",
		"/cgi-bin/responses",
		"/phpmyadmin/responses",
		"/pma/responses",
		"/vendor/phpunit/responses",
	} {
		if !ImpossiblePath(path) {
			t.Errorf("ImpossiblePath(%q) = false, want true: the frozen families are expected to claim operator-configurable base paths, which is why the owned-route carve exists", path)
		}
	}
}

// TestOwnedRootsCarveExactlyThePublishedRoutes is the carve contract. A published
// route inside an owned root reaches the data plane, a probe beside it in the same
// impossible family does not, and the carve never widens past the roots the
// generation actually publishes.
func TestOwnedRootsCarveExactlyThePublishedRoutes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		roots      []string
		path       string
		wantStatus int
		wantDenied bool
	}{
		// Published routes, carved out of the frozen families.
		{"carved create route", []string{"/wp-admin/responses"}, "/wp-admin/responses", http.StatusOK, false},
		{"carved sibling route", []string{"/wp-admin/responses"}, "/wp-admin/responses/compact", http.StatusOK, false},
		{"carved dotfile root", []string{"/.github/responses"}, "/.github/responses", http.StatusOK, false},
		{"carved trailing slash", []string{"/wp-admin/responses"}, "/wp-admin/responses/", http.StatusOK, false},
		{"carved leading slash run", []string{"/wp-admin/responses"}, "///wp-admin/responses", http.StatusOK, false},
		{"carved with query string", []string{"/wp-admin/responses"}, "/wp-admin/responses?stream=true", http.StatusOK, false},
		// A configured root is a root: everything under it is the operator's
		// surface, and a deeper published path is carved with it.
		{"carved below a root", []string{"/wp-admin"}, "/wp-admin/anything/at/all", http.StatusOK, false},
		// A repeated INTERIOR separator is not a published route. The router
		// answers it with a canonicalizing redirect rather than a handler, so the
		// carve must not claim it: the matcher normalizes only a leading run of
		// separators, and the carve compares against that same normalization.
		{"interior slash run not carved", []string{"/wp-admin/responses"}, "//wp-admin//responses", http.StatusNotFound, true},

		// Probes in the SAME family but outside the published routes stay
		// refused. This is the security value the carve must not cost.
		{"probe beside a carved route", []string{"/wp-admin/responses"}, "/wp-admin/adminer.php", http.StatusNotFound, true},
		{"probe outside a subtree carve", []string{"/wp-admin/responses"}, "/wp-admin/wp-login.php", http.StatusNotFound, true},
		{"probe beside a carved dotfile route", []string{"/.github/responses"}, "/.github/workflows", http.StatusNotFound, true},
		// A probe BELOW a carved route is also carved, because a published root
		// owns its whole subtree: a frontend base path and a diagnostics path
		// prefix both serve requests below the configured value, so an exact-only
		// carve would make a legitimate prefix mount unreachable, which is the
		// defect being fixed. The cost is bounded and fail-open in the harmless
		// direction: the router still answers these with its own 404 because no
		// handler is mounted there, and the published route itself stays guarded by
		// the auth-failure offenses and quarantine the adaptive layer keeps.
		{"probe below a carved route", []string{"/wp-admin/responses"}, "/wp-admin/responses/.env", http.StatusOK, false},
		{"probe below a carved dotfile route", []string{"/.github/responses"}, "/.github/responses/x.php", http.StatusOK, false},

		// Families with no owned root at all are untouched.
		{"unrelated family refused", []string{"/wp-admin/responses"}, "/cgi-bin/responses", http.StatusNotFound, true},
		{"unrelated family refused 2", []string{"/wp-admin/responses"}, "/phpmyadmin/responses", http.StatusNotFound, true},
		{"unrelated exact path refused", []string{"/wp-admin/responses"}, "/.env", http.StatusNotFound, true},
		{"unrelated traversal refused", []string{"/wp-admin/responses"}, "/wp-admin/../etc/passwd", http.StatusNotFound, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			state := testState(t)
			observer := &gateObserver{}
			in := testInput(t, state, observer)
			in.OwnedRoots = tc.roots
			handler := Middleware(in, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, newRequest(t, http.MethodPost, tc.path, testClient))

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			denied := len(observer.denials) == 1 && observer.denials[0] == ingressdefense.ReasonImpossiblePath
			if denied != tc.wantDenied {
				t.Errorf("impossible-path denial recorded = %v (%v), want %v", denied, observer.denials, tc.wantDenied)
			}
			if !observer.closed() {
				t.Errorf("observer recorded %d reasons outside the closed vocabulary", observer.unknownCalls)
			}
		})
	}
}

// TestOwnedRouteCarveDoesNotAccrueProbeOffense proves the carve is a routing
// decision, not a scoring decision. A request that reaches a published route must
// not be counted as an impossible-path probe, or a legitimate client on a carved
// route could quarantine itself out of a configuration that is valid by contract.
func TestOwnedRouteCarveDoesNotAccrueProbeOffense(t *testing.T) {
	t.Parallel()

	state := testState(t)
	observer := &gateObserver{}
	in := testInput(t, state, observer)
	in.OwnedRoots = []string{"/wp-admin/responses"}
	handler := Middleware(in, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(t, http.MethodPost, "/wp-admin/responses", testClient))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if state.IsQuarantined(clientAddr, testNow) {
		t.Fatal("a published route must not quarantine its own source as an impossible-path probe")
	}
	if len(observer.denials) != 0 {
		t.Fatalf("denials = %v, want none for a published route", observer.denials)
	}
}

// TestUnpublishedProbeUnderAPublishedRootStillScores keeps the two decisions
// separable within one family: the probe beside the published route is still a
// probe, so it is still refused and still scores.
func TestUnpublishedProbeUnderAPublishedRootStillScores(t *testing.T) {
	t.Parallel()

	state := testState(t)
	observer := &gateObserver{}
	in := testInput(t, state, observer)
	in.OwnedRoots = []string{"/wp-admin/responses"}
	handler := Middleware(in, neverDownstream(t))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(t, http.MethodGet, "/wp-admin/.env", testClient))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !state.IsQuarantined(clientAddr, testNow) {
		t.Fatal("a refused probe must still score a quarantine")
	}
	if len(observer.denials) != 1 || observer.denials[0] != ingressdefense.ReasonImpossiblePath {
		t.Fatalf("denials = %v, want exactly one impossible-path denial", observer.denials)
	}
}

// TestAbsentOrUnusableOwnedRootsKeepTheFrozenFamiliesRefused is the
// absent-inventory posture. A generation that publishes no owned roots, or only
// unusable ones, keeps exactly the pre-carve behavior, so a missing or empty
// inventory can never silently open the families.
func TestAbsentOrUnusableOwnedRootsKeepTheFrozenFamiliesRefused(t *testing.T) {
	t.Parallel()

	for _, roots := range [][]string{nil, {}, {""}, {"  "}, {"/"}, {"relative/path"}} {
		state := testState(t)
		in := testInput(t, state, &gateObserver{})
		in.OwnedRoots = roots
		handler := Middleware(in, neverDownstream(t))

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(t, http.MethodPost, "/wp-admin/responses", testClient))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("roots %q: status = %d, want 404", roots, rec.Code)
		}
	}
}

// TestOwnedRootsAreMatchedCaseInsensitivelyLikeTheFamilies keeps the carve and the
// rules in one equivalence class. The frozen families fold ASCII case, so a carve
// that folded less would leave a published route refusable by changing its case.
func TestOwnedRootsAreMatchedCaseInsensitivelyLikeTheFamilies(t *testing.T) {
	t.Parallel()

	state := testState(t)
	observer := &gateObserver{}
	in := testInput(t, state, observer)
	in.OwnedRoots = []string{"/wp-admin/responses"}
	handler := Middleware(in, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/WP-ADMIN/responses", "/Wp-Admin/Responses"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(t, http.MethodPost, path, testClient))
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200: the carve must fold case like the rules do", path, rec.Code)
		}
	}
}
