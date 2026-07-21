package configsource

import (
	"errors"
	"fmt"
)

// IntegrityError is a secret-safe source-integrity or pre-decode failure.
// Error strings carry only the category (and bounded limits), never raw bytes.
type IntegrityError struct {
	Category Category
	Limit    int64 // set for oversize; otherwise 0
}

func (e *IntegrityError) Error() string {
	if e == nil {
		return "configsource: unknown"
	}
	if e.Category == CategoryOversize && e.Limit > 0 {
		return fmt.Sprintf("configsource: %s (limit %d bytes)", e.Category, e.Limit)
	}
	return fmt.Sprintf("configsource: %s", e.Category)
}

// CategoryOf returns the integrity/decode category when err wraps or is an
// IntegrityError or carries a known configsource category prefix.
func CategoryOf(err error) (Category, bool) {
	if err == nil {
		return "", false
	}
	var ie *IntegrityError
	if errors.As(err, &ie) && ie != nil && ie.Category != "" {
		return ie.Category, true
	}
	return "", false
}

func integrityErr(cat Category) error {
	return &IntegrityError{Category: cat}
}

func oversizeErr(limit int64) error {
	return &IntegrityError{Category: CategoryOversize, Limit: limit}
}
