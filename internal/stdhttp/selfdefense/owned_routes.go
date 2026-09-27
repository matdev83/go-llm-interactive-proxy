package selfdefense

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
)

// ownedRouteProbeSegment is appended to a candidate path to discover whether the
// router also registered that path as a subtree. Several protected operator mounts
// register BOTH the bare path and the path with a trailing separator, so probing
// the path alone would discover only the exact registration and silently leave the
// subtree it also owns uncarved.
const ownedRouteProbeSegment = "x"

// OwnedRoutesFromMux resolves which of the candidates the router actually owns, and
// with which match semantics, by asking the router itself.
//
// Ownership is a property of the router, not of the configuration that produced the
// candidate, so this is resolved against the real *http.ServeMux rather than
// inferred from a path's shape. That is what makes the carve faithful:
//
//   - a configured path whose feature is disabled is not mounted, resolves to no
//     dedicated pattern, and therefore carves nothing;
//   - a literal registration is recorded as exact, and a trailing-slash
//     registration is recorded as a subtree, because that is what the router
//     actually does with it;
//   - the method is carried through, so a route registered for one method is not
//     treated as owning the other methods.
//
// The router's fallback is never ownership: every path it does not route resolves
// to a fallback handler, so a fallback pattern would otherwise carve the entire data
// plane. The bare root pattern and the empty pattern are therefore dropped.
//
// Resolution happens once per generation, at composition, and never on the request
// path.
func OwnedRoutesFromMux(mux *http.ServeMux, candidates []httpcontract.OwnedRouteCandidate) []httpcontract.OwnedRoute {
	if mux == nil || len(candidates) == 0 {
		return nil
	}
	var owned []httpcontract.OwnedRoute
	seen := make(map[httpcontract.OwnedRoute]struct{}, len(candidates)*2)
	record := func(pattern string) {
		method, routePath, subtree := splitDedicatedPattern(pattern)
		if routePath == "" {
			return
		}
		route := httpcontract.OwnedRoute{Method: method, Path: routePath, Subtree: subtree}
		if _, duplicate := seen[route]; duplicate {
			return
		}
		seen[route] = struct{}{}
		owned = append(owned, route)
	}
	for _, candidate := range candidates {
		path := normalizeOwnedPath(candidate.Path)
		if path == "" {
			continue
		}
		// An absent candidate method means the registration is method-less, so it
		// answers every method. The router still needs a concrete method to resolve
		// against, and a method-less pattern answers GET, so GET is the faithful
		// probe; the resolved pattern's own method is what gets recorded.
		method := normalizeOwnedMethod(candidate.Method)
		if method == "" {
			method = http.MethodGet
		}
		// The candidate itself, which reveals an exact registration, and a subtree
		// registration of the same path, which the router answers with its own
		// trailing-slash pattern.
		for _, probe := range []string{path, path + "/" + ownedRouteProbeSegment} {
			record(dedicatedPatternFor(mux, method, probe))
		}
	}
	slices.SortFunc(owned, compareOwnedRoutes)
	return owned
}

// OwnedRoutes is an immutable, pre-compiled set of the routes the running
// generation owns. It is built once when the gate is constructed, so deciding
// whether a path is owned costs a slice comparison and allocates nothing: an
// attacker chooses how often the branch that consults it runs, so the work must not
// be per-request proportional to the inventory size.
type OwnedRoutes struct {
	routes []httpcontract.OwnedRoute
}

// NewOwnedRoutes compiles the inventory into its immutable form. A route that is
// empty, blank, relative, or the root path itself owns nothing and is dropped, so a
// degenerate inventory cannot widen the carve to the whole data plane.
func NewOwnedRoutes(routes []httpcontract.OwnedRoute) OwnedRoutes {
	compiled := make([]httpcontract.OwnedRoute, 0, len(routes))
	for _, route := range routes {
		path := normalizeOwnedPath(route.Path)
		if path == "" || path == "/" {
			continue
		}
		if !route.Subtree {
			path = strings.TrimSuffix(path, "/")
			if path == "" {
				continue
			}
		}
		compiled = append(compiled, httpcontract.OwnedRoute{
			Method:  normalizeOwnedMethod(route.Method),
			Path:    path,
			Subtree: route.Subtree,
		})
	}
	if len(compiled) == 0 {
		return OwnedRoutes{}
	}
	slices.SortFunc(compiled, compareOwnedRoutes)
	return OwnedRoutes{routes: slices.CompactFunc(compiled, sameOwnedRoute)}
}

// Covers reports whether the router owns this exact request target, using the
// router's own semantics: case-sensitive comparison, an exact registration matching
// only its own method and path, and a subtree registration matching everything
// below its path.
//
// Matching is case-SENSITIVE on purpose. http.ServeMux matches literal patterns
// case-sensitively, so /WP-ADMIN/responses is not the route /wp-admin/responses and
// is correctly an impossible-path probe rather than a carved request.
func (o OwnedRoutes) Covers(method, path string) bool {
	if len(o.routes) == 0 {
		return false
	}
	target := normalizeOwnedPath(path)
	if target == "" {
		return false
	}
	for _, route := range o.routes {
		if route.Method != "" && route.Method != method {
			continue
		}
		if route.Subtree {
			// route.Path already ends in a separator, so a prefix match is a
			// descendant. The bare root of the subtree is owned too, because the router
			// answers it with a canonicalizing redirect rather than falling through.
			if strings.HasPrefix(target, route.Path) || target == strings.TrimSuffix(route.Path, "/") {
				return true
			}
			continue
		}
		if target == route.Path {
			return true
		}
	}
	return false
}

// Len reports the number of owned routes, for diagnostics and tests.
func (o OwnedRoutes) Len() int { return len(o.routes) }

// dedicatedPatternFor asks the router which pattern owns this method/path, and
// returns "" when the answer is the router's fallback rather than a real
// registration.
func dedicatedPatternFor(mux *http.ServeMux, method, path string) string {
	req := &http.Request{Method: method, URL: &url.URL{Path: path}}
	_, pattern := mux.Handler(req)
	return pattern
}

// isDedicatedPattern reports whether a resolved pattern is a real registration
// rather than the router's fallback. Both the empty pattern (nothing registered at
// all) and the bare root "/" mean "this path is not routed anywhere specific".
func isDedicatedPattern(pattern string) bool {
	_, path, _ := splitDedicatedPattern(pattern)
	return path != ""
}

// splitDedicatedPattern splits a Go 1.22+ ServeMux pattern of the form
// "METHOD PATH" (or a bare "PATH"), and reports whether the path is a trailing-slash
// subtree registration. It returns an empty path for a fallback pattern.
func splitDedicatedPattern(pattern string) (method, path string, subtree bool) {
	if pattern == "" {
		return "", "", false
	}
	// A method-less registration is a bare "PATH"; a method-scoped one is
	// "METHOD PATH". Defaulting path to the whole pattern is what keeps a
	// method-less registration, which is most of them, from being dropped.
	method, path = "", pattern
	if before, rest, found := strings.Cut(pattern, " "); found {
		method, path = before, strings.TrimSpace(rest)
	}
	if path == "" || path == "/" {
		return "", "", false
	}
	if strings.HasSuffix(path, "/") {
		return method, path, true
	}
	return method, path, false
}

// normalizeOwnedPath reduces a path to the absolute, single-slash-normalized form the
// router compares against. It is the same normalization the frozen families use, so
// the carve and the rules agree on what a path is.
func normalizeOwnedPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if cut := strings.IndexByte(path, '?'); cut >= 0 {
		path = path[:cut]
	}
	if count := leadingSlashCount(path); count > 1 {
		path = path[count-1:]
	}
	if path == "/" {
		return ""
	}
	return path
}

// normalizeOwnedMethod upper-cases a method and reduces an absent one to the empty
// string, which matches every method the way a registration without a method does.
func normalizeOwnedMethod(method string) string {
	return strings.ToUpper(strings.TrimSpace(method))
}

func compareOwnedRoutes(a, b httpcontract.OwnedRoute) int {
	if a.Path != b.Path {
		if a.Path < b.Path {
			return -1
		}
		return 1
	}
	if a.Method != b.Method {
		if a.Method < b.Method {
			return -1
		}
		return 1
	}
	if a.Subtree == b.Subtree {
		return 0
	}
	if a.Subtree {
		return 1
	}
	return -1
}

func sameOwnedRoute(a, b httpcontract.OwnedRoute) bool { return a == b }
