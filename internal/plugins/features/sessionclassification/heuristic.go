package sessionclassification

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/matdev83/go-llm-interactive-proxy/internal/agentfacts"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	sdkclassification "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
)

// LocalDecision contains bounded policy facts without manufacturing persisted
// classification state or a revision.
type LocalDecision struct {
	Promotes     bool
	Preserves    bool
	Source       session.ClassificationSource
	EvidenceCode session.EvidenceCode
	ClientFamily agentfacts.Family
}

const (
	codeToolingDistinctCluster = session.EvidenceCode("tooling.distinct_coding_cluster")
	codeToolingProjectMarker   = session.EvidenceCode("tooling.project_marker_cluster")
)

// EvaluateLocal evaluates accepted client identity, canonical tool categories,
// and the bounded recognized marker snapshot. It never reads transcript,
// model, route, workspace root, or raw tool-name data.
func EvaluateLocal(cfg Config, input sdkclassification.Input) LocalDecision {
	if input.Session.Classification.IsCodingAgent() {
		prior := input.Session.Classification
		return LocalDecision{
			Preserves:    true,
			Source:       prior.Source,
			EvidenceCode: prior.Evidence,
		}
	}

	mode := cfg.Mode
	if mode == "" {
		mode = ModeHeuristic
	}
	if mode != ModeHeuristic && mode != ModeJev && mode != ModeHybrid {
		return LocalDecision{}
	}

	if !isIgnoredUserAgent(input.Evidence.ClientUserAgent, cfg.Heuristic.IgnoredUserAgentPrefixes) {
		if match, ok := agentfacts.MatchIdentity(input.Evidence.ClientUserAgent); ok && match.Confidence == agentfacts.ConfidenceHigh {
			code := clientFamilyEvidenceCode(match.Family)
			if code != "" {
				return decisionForEvidence(mode, session.SourceLocalIdentity, code, match.Family)
			}
		}
	}

	categories := input.Evidence.ToolCategories
	hasReadOrSearch := categories&(sdkclassification.ToolCategoryFileRead|sdkclassification.ToolCategoryFileSearch) != 0
	hasEditOrRemove := categories&(sdkclassification.ToolCategoryFileEdit|sdkclassification.ToolCategoryFileRemove) != 0
	hasOSCommand := categories&sdkclassification.ToolCategoryOSCommand != 0
	if hasReadOrSearch && hasEditOrRemove && hasOSCommand {
		return decisionForEvidence(mode, session.SourceLocalTooling, codeToolingDistinctCluster, "")
	}
	if hasReadOrSearch && (hasEditOrRemove || hasOSCommand) && hasRecognizedProjectMarker(input.Workspace.Markers) {
		return decisionForEvidence(mode, session.SourceLocalTooling, codeToolingProjectMarker, "")
	}
	return LocalDecision{}
}

func decisionForEvidence(mode Mode, source session.ClassificationSource, code session.EvidenceCode, family agentfacts.Family) LocalDecision {
	decision := LocalDecision{Source: source, EvidenceCode: code, ClientFamily: family}
	if mode != ModeJev {
		decision.Promotes = true
	}
	return decision
}

func clientFamilyEvidenceCode(family agentfacts.Family) session.EvidenceCode {
	switch family {
	case agentfacts.FamilyCodex:
		return "client_family.codex"
	case agentfacts.FamilyRoo:
		return "client_family.roo"
	case agentfacts.FamilyOpenCode:
		return "client_family.opencode"
	case agentfacts.FamilyPi:
		return "client_family.pi"
	case agentfacts.FamilyDroid:
		return "client_family.droid"
	case agentfacts.FamilyHermes:
		return "client_family.hermes"
	default:
		return ""
	}
}

func isIgnoredUserAgent(userAgent string, prefixes []string) bool {
	if userAgent == "" {
		return false
	}
	normalized := strings.ToLower(userAgent)
	limit := len(prefixes)
	if limit > MaxIgnoredUserAgentPrefixes {
		limit = MaxIgnoredUserAgentPrefixes
	}
	for i := 0; i < limit; i++ {
		prefix := prefixes[i]
		if len(prefix) == 0 || len(prefix) > MaxIgnoredUserAgentPrefixBytes {
			continue
		}
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix != "" && strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func hasRecognizedProjectMarker(markers []string) bool {
	limit := len(markers)
	if limit > MaxWorkspaceMarkers {
		limit = MaxWorkspaceMarkers
	}
	for i := 0; i < limit; i++ {
		marker := markers[i]
		if len(marker) == 0 || len(marker) > MaxWorkspaceMarkerBytes || !validMarkerName(marker) {
			continue
		}
		marker = strings.ToLower(marker)
		switch marker {
		case "go.mod", "go.work", "cargo.toml", "pyproject.toml", "package.json",
			"pom.xml", "build.gradle", "build.gradle.kts", "composer.json", "gemfile":
			return true
		}
		if hasProjectSuffix(marker) {
			return true
		}
	}
	return false
}

func hasProjectSuffix(marker string) bool {
	for _, suffix := range [...]string{".sln", ".slnx", ".csproj", ".fsproj", ".vbproj", ".vcxproj", ".proj", ".xcodeproj", ".xcworkspace"} {
		if len(marker) > len(suffix) && strings.HasSuffix(marker, suffix) {
			return true
		}
	}
	return false
}

func validMarkerName(marker string) bool {
	if !utf8.ValidString(marker) || strings.ContainsAny(marker, "/\\") {
		return false
	}
	for _, r := range marker {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
