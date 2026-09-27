// Package selfdefense is the standard-HTTP driving adapter for ingress
// self-defense. It owns the request-side classification (fixed impossible-path
// matching and adaptive quarantine lookup) and the generic refusal responses;
// the provider/protocol-neutral state machine lives in
// internal/core/ingressdefense and the transport-auth outcome observation lives
// in internal/stdhttp/auth.
//
// The gate runs outside tracing, general HTTP metrics, request ID, access
// logging, transport auth and the route mux, so a deterministic probe refusal
// and an adaptive quarantine refusal both cost one address parse, one fixed
// matcher pass and at most one bounded state lookup. It performs no DNS lookup,
// no reputation query, no feed download and no database or other
// request-driven network I/O.
package selfdefense

import "strings"

// maxAuditedImpossiblePathRules bounds the frozen v1 set. The set is deliberately
// small and static: it is a curated list of commodity exploit probes, not a
// signature downloader, CVE database, regex/rule DSL or scanner interpreter.
// Extending it is ordinary maintenance after a false-positive review.
const maxAuditedImpossiblePathRules = 64

// impossibleExactPaths are audited whole-path probes: the normalized request
// path must equal one entry, compared ASCII case-insensitively.
//
// The standard distribution serves no file tree, so a request for a dotfile or a
// PHP/CGI control page can never reach a route, a frontend or a model.
var impossibleExactPaths = []string{
	"/adminer.php",
	"/composer.json",
	"/composer.lock",
	"/info.php",
	"/phpinfo.php",
	"/phpunit.xml",
	"/phpunit.xml.dist",
	"/server-info",
	"/server-status",
	"/test.php",
	"/web.config",
	"/wp-cron.php",
	"/wp-login.php",
	"/xmlrpc.php",
}

// impossiblePrefixPaths are audited path-anchored families: the request path
// must equal one entry or begin with the entry followed by "/". Each family is
// an application surface the standard AIProxer data plane never mounts.
var impossiblePrefixPaths = []string{
	"/cgi-bin",
	"/pma",
	"/phpmyadmin",
	"/adminer",
	"/vendor/phpunit",
	"/wp-admin",
	"/wp-content",
	"/wp-includes",
}

// impossibleSegmentPrefixes are audited dotfile/VCS markers: some path segment
// must begin with one entry. The leading dot keeps ordinary routes such as
// "/v1/environment" or "/v1/git/config" out of the set while covering the
// position variants commodity scanners actually use ("/v1/.env").
var impossibleSegmentPrefixes = []string{
	".aws",
	".ds_store",
	".docker",
	".editorconfig",
	".env",
	".git",
	".hg",
	".ht",
	".idea",
	".kube",
	".npmrc",
	".ssh",
	".svn",
}

// ImpossiblePath reports whether the decoded request-target path cannot plausibly
// belong to the standard data plane, given the routes this generation publishes.
//
// path must be the decoded path ([url.URL.Path]) of the request target, which
// net/http has already percent-decoded exactly once; passing a raw escaped
// target is a caller error. Matching uses only this path structure: it never
// reads the request body, canonical messages or items, tool arguments, SQL or
// code text, and never an arbitrary query value, and it retains no part of the
// path.
//
// ownedRoots are the data-plane routes this generation actually publishes. A
// published route is authoritative: the configuration and compiler layers accept
// an arbitrary normalized absolute base path, so an operator may legitimately
// mount a frontend, diagnostics or metrics surface inside one of the audited
// families, and a frozen family must never make such a route unreachable. The
// carve is therefore applied to the MATCHED path before the refusal decision, and
// it is exact per published route: a probe beside a published route, in the same
// family, is still refused and still scores. An empty or absent inventory keeps
// the pre-carve behavior, so a missing projection can never open the families.
//
// A decoded path can itself contain a question mark, because one escaped "?" in
// the request target decodes into a literal "?" plus the attacker's remaining
// text. normalizeTarget therefore cuts at the first "?" itself, so
// query-independence is a structural property of the matcher rather than a caller
// obligation, and no rule family can reintroduce the leak. The accepted trade-off
// is that a probe smuggled behind an escaped "?" is treated as query text and
// therefore evades this deterministic classifier and accrues no adaptive probe
// offense; it still reaches no route, because the router sees the same decoded
// "?" inside the path.
//
// The decision is deterministic and allocation-free on the standard surface. It
// is case-insensitive over ASCII and ignores repeated leading slashes, because
// the standard distribution is case-sensitive and never normalizes a target
// into any of these families.
func ImpossiblePath(path string, ownedRoots ...string) bool {
	if path == "" {
		return false
	}
	target := normalizeTarget(path)
	if target == "/" {
		return false
	}
	if hasTraversal(target) {
		return true
	}
	if !matchesAnyRule(target) {
		return false
	}
	if len(ownedRoots) == 0 {
		return true
	}
	// Roots are normalized here, after a family already matched, so the ordinary
	// request path never pays for the inventory and no caller can pass a root in a
	// form this function would silently misread.
	return !underOwnedRoot(target, normalizeOwnedRoots(ownedRoots))
}

// matchesAnyRule reports whether the normalized target matches any audited family.
// It is the whole frozen rule set, and it is deliberately separate from the owned
// route decision so the two can never influence each other.
func matchesAnyRule(target string) bool {
	for _, rule := range impossibleExactPaths {
		if matchesRootPath(target, rule) {
			return true
		}
	}
	for _, rule := range impossiblePrefixPaths {
		if matchesRootPrefix(target, rule) {
			return true
		}
	}
	for _, rule := range impossibleSegmentPrefixes {
		if hasSegmentPrefix(target, rule) {
			return true
		}
	}
	return false
}

// underOwnedRoot reports whether the normalized target is a published route.
//
// A root owns itself and its whole subtree, because a published route is not
// always a single path: a frontend base path, a diagnostics path prefix and a
// metrics mount all serve requests below the configured value. Comparison folds
// ASCII case exactly like the rule families do, so the carve and the rules share
// one equivalence class and a published route cannot become refusable by changing
// its case.
//
// roots must already be normalized by [normalizeOwnedRoots], so this stays
// allocation-free and runs only after a frozen family matched.
func underOwnedRoot(target string, roots []string) bool {
	for _, root := range roots {
		if foldEqual(target, root) {
			return true
		}
		if len(target) > len(root) && foldEqual(target[:len(root)], root) && target[len(root)] == '/' {
			return true
		}
	}
	return false
}

// normalizeOwnedRoots reduces a generation's published-route inventory to the form
// the carve compares against, once, when the gate is built.
//
// Normalization is the same normalization the matcher applies to a request target,
// so the carve and the rules agree on what a path is. A root that is empty, blank,
// relative, or the root path itself owns nothing, so a degenerate inventory cannot
// widen the carve to the whole data plane. The result aliases no caller state and
// is never mutated after construction.
func normalizeOwnedRoots(ownedRoots []string) []string {
	if len(ownedRoots) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(ownedRoots))
	for _, root := range ownedRoots {
		candidate := normalizeTarget(strings.TrimSpace(root))
		if candidate == "" || candidate == "/" || !strings.HasPrefix(candidate, "/") {
			continue
		}
		normalized = append(normalized, candidate)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// normalizeTarget is the single normalization entry point every rule family
// reads its target from.
//
// It first discards the first "?" and everything after it, so a decoded question
// mark cannot smuggle a probe into a legitimate path, and then applies the shared
// slash normalization. Both steps are slice-only, so normalization allocates
// nothing and a single call site keeps the families from diverging.
func normalizeTarget(path string) string {
	if cut := strings.IndexByte(path, '?'); cut >= 0 {
		path = path[:cut]
	}
	return normalizedRoot(trimTrailingSlashes(path))
}

// trimTrailingSlashes removes trailing path separators so a mounted family
// matches with or without the empty trailing segment. "/" is preserved.
func trimTrailingSlashes(path string) string {
	trimmed := strings.TrimRight(path, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

// hasTraversal reports whether the target contains a parent-directory segment or
// a backslash separator. Neither can occur in a normalized standard route:
// contract.NormalizePath rejects a traversal segment and a backslash outright, and
// net/http never treats a backslash as a path separator, so both forms are file
// traversal probes rather than data-plane requests.
func hasTraversal(path string) bool {
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '\\':
			return true
		case '.':
			if i+1 >= len(path) || path[i+1] != '.' {
				continue
			}
			startOfSegment := i == 0 || path[i-1] == '/'
			endOfSegment := i+2 == len(path) || path[i+2] == '/'
			if startOfSegment && endOfSegment {
				return true
			}
		}
	}
	return false
}

// matchesRootPath compares the whole normalized target against a lowercase exact
// rule.
func matchesRootPath(target, rule string) bool {
	if len(target) != len(rule) {
		return false
	}
	return foldEqual(target, rule)
}

// matchesRootPrefix compares the normalized target against a lowercase
// path-anchored rule. The rule must cover the whole first segment: "/pma" matches
// "/pma" and "/pma/index.php" but never "/v1/pma".
func matchesRootPrefix(target, rule string) bool {
	if len(target) < len(rule) || !foldEqual(target[:len(rule)], rule) {
		return false
	}
	return len(target) == len(rule) || target[len(rule)] == '/'
}

// hasSegmentPrefix reports whether some path segment begins with a lowercase
// dot-prefixed marker, at any depth.
func hasSegmentPrefix(target, marker string) bool {
	for rest := target; ; {
		end := strings.IndexByte(rest, '/')
		segment := rest
		if end >= 0 {
			segment, rest = rest[:end], rest[end+1:]
		}
		if len(segment) >= len(marker) && foldEqual(segment[:len(marker)], marker) {
			return true
		}
		if end < 0 {
			return false
		}
	}
}

// normalizedRoot collapses a run of leading slashes to exactly one so a repeated
// separator cannot hide a rule, and drops the empty root target. It returns a
// slice of path and allocates nothing.
func normalizedRoot(path string) string {
	count := leadingSlashCount(path)
	if count == 0 {
		return path
	}
	return path[count-1:]
}

// foldEqual compares s against a rule that is already stored lowercase. Every
// audited rule is lowercase, so a single ASCII fold is sufficient and the
// comparison allocates nothing.
func foldEqual(s, rule string) bool {
	if len(s) != len(rule) {
		return false
	}
	for i := 0; i < len(rule); i++ {
		if asciiLower(s[i]) != rule[i] {
			return false
		}
	}
	return true
}

func leadingSlashCount(path string) int {
	count := 0
	for count < len(path) && path[count] == '/' {
		count++
	}
	return count
}

func asciiLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}
