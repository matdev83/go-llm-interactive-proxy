package contract

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"
)

// RouteKind is an opaque extension-owned operation identifier.
type RouteKind string

// CanonicalLegacyBasePath is the shared /v1 prefix used by existing frontends.
const CanonicalLegacyBasePath = "/v1"

// RouteClaim registers one normalized HTTP route owner before serving.
type RouteClaim struct {
	OwnerID string
	Method  string
	Path    string
	Kind    RouteKind
}

func (k RouteKind) Validate() error {
	s := string(k)
	if s == "" || len(s) > 96 {
		return fmt.Errorf("route claim: invalid kind")
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-", r) {
			return fmt.Errorf("route claim: invalid kind %q", s)
		}
	}
	return nil
}

// NormalizeMethod uppercases and trims an HTTP method.
func NormalizeMethod(method string) (string, error) {
	m := strings.TrimSpace(strings.ToUpper(method))
	if m == "" {
		return "", fmt.Errorf("route claim: empty method")
	}
	if strings.ContainsAny(m, "\r\n\t") {
		return "", fmt.Errorf("route claim: method contains control characters")
	}
	if m != http.MethodGet && m != http.MethodPost && m != http.MethodPut &&
		m != http.MethodPatch && m != http.MethodDelete && m != http.MethodOptions {
		return "", fmt.Errorf("route claim: unsupported method %q", method)
	}
	return m, nil
}

// NormalizePath canonicalizes a mount path for deterministic ownership checks.
func NormalizePath(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", fmt.Errorf("route claim: empty path")
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("route claim: path must be absolute")
	}
	if strings.ContainsAny(p, "?#*\\") {
		return "", fmt.Errorf("route claim: path contains invalid characters (query/fragment/wildcard/backslash)")
	}
	if strings.Contains(p, "//") {
		return "", fmt.Errorf("route claim: path contains double slash")
	}
	segments := strings.SplitSeq(p, "/")
	for seg := range segments {
		if seg == "." || seg == ".." {
			return "", fmt.Errorf("route claim: path contains traversal segment %q", seg)
		}
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	if p == "" {
		p = "/"
	}
	return p, nil
}

// NormalizedClaim returns a copy with normalized method and path.
func (c RouteClaim) NormalizedClaim() (RouteClaim, error) {
	method, err := NormalizeMethod(c.Method)
	if err != nil {
		return RouteClaim{}, err
	}
	path, err := NormalizePath(c.Path)
	if err != nil {
		return RouteClaim{}, err
	}
	if strings.TrimSpace(c.OwnerID) == "" {
		return RouteClaim{}, fmt.Errorf("route claim: empty owner id")
	}
	if err := c.Kind.Validate(); err != nil {
		return RouteClaim{}, err
	}
	out := c
	out.Method = method
	out.Path = path
	out.OwnerID = strings.TrimSpace(c.OwnerID)
	return out, nil
}

// ClaimsForBasePath builds claims for a frontend-owned operation set. The
// contract validates and normalizes generic ownership data; protocol packages
// own operation identifiers and route paths.
func ClaimsForBasePath(ownerID, basePath string, operations ...RouteClaim) ([]RouteClaim, error) {
	base, err := NormalizePath(basePath)
	if err != nil {
		return nil, err
	}
	out := make([]RouteClaim, 0, len(operations))
	for _, operation := range operations {
		operation.OwnerID = ownerID
		operation.Path = strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(operation.Path, "/")
		normalized, err := operation.NormalizedClaim()
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}
