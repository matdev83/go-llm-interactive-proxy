package configreload

import (
	"errors"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// MapLoadFailure maps effective-load / source-integrity failures to a terminal
// result category and bounded reason (req 2.10, 3.10). Values are never included.
func MapLoadFailure(err error) (ResultCategory, string) {
	if err == nil {
		return ResultInternalFailed, "unknown"
	}
	var rr *RestartRequiredError
	if errors.As(err, &rr) {
		return ResultRestartRequired, StageClassify
	}
	var le *config.LoadError
	if errors.As(err, &le) && le != nil && le.Category != "" {
		return mapLoadCategory(string(le.Category))
	}
	// configsource.IntegrityError and similar expose Category via Error() prefix
	// or errors.As; callers may also pass CategoryOf results through ReasonCategory.
	msg := err.Error()
	switch {
	case strings.Contains(msg, string(config.CategoryMissing)),
		strings.Contains(msg, string(config.CategoryEmpty)),
		strings.Contains(msg, string(config.CategoryWhitespace)),
		strings.Contains(msg, string(config.CategoryOversize)),
		strings.Contains(msg, string(config.CategoryUnstable)),
		strings.Contains(msg, string(config.CategoryNonAtomicUpdate)),
		strings.Contains(msg, string(config.CategoryUnsupportedType)),
		strings.Contains(msg, string(config.CategoryPartialUnreadable)):
		return ResultSourceIntegrity, StageRead
	case strings.Contains(msg, string(config.CategoryMalformedYAML)),
		strings.Contains(msg, string(config.CategoryMultipleDocuments)),
		strings.Contains(msg, string(config.CategoryTrailingContent)),
		strings.Contains(msg, string(config.CategoryUnknownCoreField)):
		return ResultInvalid, StageLoad
	case strings.Contains(msg, "validate"):
		return ResultInvalid, StageLoad
	default:
		return ResultInvalid, StageLoad
	}
}

func mapLoadCategory(cat string) (ResultCategory, string) {
	switch config.LoadCategory(cat) {
	case config.CategoryMissing, config.CategoryEmpty, config.CategoryWhitespace,
		config.CategoryOversize, config.CategoryUnstable, config.CategoryNonAtomicUpdate,
		config.CategoryUnsupportedType, config.CategoryPartialUnreadable:
		return ResultSourceIntegrity, StageRead
	case config.CategoryMalformedYAML, config.CategoryMultipleDocuments,
		config.CategoryTrailingContent, config.CategoryUnknownCoreField:
		return ResultInvalid, StageLoad
	default:
		return ResultInvalid, StageLoad
	}
}

// MapLoadCategory maps a known LoadCategory string to a terminal result.
func MapLoadCategory(cat string) (ResultCategory, string) {
	if cat == "" {
		return ResultInternalFailed, "unknown"
	}
	return mapLoadCategory(cat)
}
