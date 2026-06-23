package ollama

import (
	"fmt"
	"strconv"
	"strings"
)

type Semver struct {
	Major int
	Minor int
	Patch int
}

const minResponsesVersion = "0.13.3"

func NativeRootFromBaseURL(baseURL string) string {
	u := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(strings.ToLower(u), "/v1") {
		return strings.TrimSuffix(u, "/v1")
	}
	return u
}

func ParseSemver(raw string) (Semver, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Semver{}, fmt.Errorf("ollama: empty version")
	}
	if idx := strings.IndexAny(raw, "-+"); idx >= 0 {
		raw = raw[:idx]
	}
	parts := strings.Split(raw, ".")
	if len(parts) == 0 {
		return Semver{}, fmt.Errorf("ollama: invalid version %q", raw)
	}
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return Semver{}, fmt.Errorf("ollama: invalid version %q", raw)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return Semver{}, fmt.Errorf("ollama: invalid version %q", raw)
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return Semver{}, fmt.Errorf("ollama: invalid version %q", raw)
	}
	return Semver{Major: major, Minor: minor, Patch: patch}, nil
}

func MustParseSemver(raw string) Semver {
	v, err := ParseSemver(raw)
	if err != nil {
		panic(err)
	}
	return v
}

func (v Semver) AtLeast(min Semver) bool {
	if v.Major != min.Major {
		return v.Major > min.Major
	}
	if v.Minor != min.Minor {
		return v.Minor > min.Minor
	}
	return v.Patch >= min.Patch
}

func VersionSupportsResponses(version string) bool {
	v, err := ParseSemver(version)
	if err != nil {
		return false
	}
	return v.AtLeast(MustParseSemver(minResponsesVersion))
}
