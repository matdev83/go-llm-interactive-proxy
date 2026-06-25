package anthropicmessages

import (
	"errors"
	"net/http"
	"strings"

	asdk "github.com/anthropics/anthropic-sdk-go"
)

type apiFailureKind int

const (
	apiFailureNone apiFailureKind = iota
	apiFailureRateLimited
	apiFailureAuthInvalid
)

func classifyAnthropicAPIError(err error) (kind apiFailureKind, retryAfter string) {
	var apiErr *asdk.Error
	if err == nil || !errors.As(err, &apiErr) || apiErr == nil {
		return apiFailureNone, ""
	}
	switch apiErr.StatusCode {
	case http.StatusUnauthorized:
		return apiFailureAuthInvalid, ""
	case http.StatusTooManyRequests:
		ra := ""
		if apiErr.Response != nil {
			ra = strings.TrimSpace(apiErr.Response.Header.Get("Retry-After"))
		}
		return apiFailureRateLimited, ra
	default:
		return apiFailureNone, ""
	}
}
