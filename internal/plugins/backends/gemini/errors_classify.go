package gemini

import (
	"errors"
	"net/http"
	"strings"

	"google.golang.org/genai"
)

type apiFailureKind int

const (
	apiFailureNone apiFailureKind = iota
	apiFailureRateLimited
	apiFailureAuthInvalid
)

func classifyGenaiAPIError(err error) (kind apiFailureKind, retryAfter string) {
	var ae genai.APIError
	if errors.As(err, &ae) {
		return classifyGenaiCode(ae.Code, "")
	}
	var pae *genai.APIError
	if errors.As(err, &pae) && pae != nil {
		return classifyGenaiCode(pae.Code, "")
	}
	return apiFailureNone, ""
}

func classifyGenaiCode(code int, retryAfter string) (apiFailureKind, string) {
	switch code {
	case http.StatusUnauthorized:
		return apiFailureAuthInvalid, ""
	case http.StatusTooManyRequests:
		return apiFailureRateLimited, strings.TrimSpace(retryAfter)
	default:
		return apiFailureNone, ""
	}
}
