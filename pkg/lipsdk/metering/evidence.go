package metering

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Safe evidence locations are a finite, provider-neutral contract. Provider
// normalizers own mapping from wire fields into these exact locations. A new
// provider field must use a versioned normalizer; arbitrary paths and header
// prefixes are not a retention mechanism.
var safeEvidencePathAllowlist = map[string]struct{}{
	"$.usage.input_tokens":                   {},
	"$.usage.output_tokens":                  {},
	"$.usage.total_tokens":                   {},
	"$.usage.cache_read_input_tokens":        {},
	"$.usage.cache_write_input_tokens":       {},
	"$.usage.reasoning_output_tokens":        {},
	"$.usage.tool_use_prompt_tokens":         {},
	"$.usage.audio_seconds":                  {},
	"$.usage.video_seconds":                  {},
	"$.usage.image_count":                    {},
	"$.usage.input_image_count":              {},
	"$.usage.output_image_count":             {},
	"$.usage.input_image_tokens":             {},
	"$.usage.output_image_tokens":            {},
	"$.usage.input_text_tokens":              {},
	"$.usage.output_text_tokens":             {},
	"$.usage.input_audio_tokens":             {},
	"$.usage.output_audio_tokens":            {},
	"$.usage.input_audio_seconds":            {},
	"$.usage.output_audio_seconds":           {},
	"$.usage.input_video_tokens":             {},
	"$.usage.output_video_tokens":            {},
	"$.usage.input_video_seconds":            {},
	"$.usage.output_video_seconds":           {},
	"$.usage.input_video_frames":             {},
	"$.usage.output_video_frames":            {},
	"$.usage.input_document_tokens":          {},
	"$.usage.output_document_tokens":         {},
	"$.usage.input_document_pages":           {},
	"$.usage.output_document_pages":          {},
	"$.usage.cache_creation_5m_input_tokens": {},
	"$.usage.cache_creation_1h_input_tokens": {},
	"$.usage.web_fetch_requests":             {},
	"$.usage.web_search_requests":            {},
	"$.usage.input_file_pages":               {},
	"$.usage.output_file_pages":              {},
	"$.usage.input_bytes":                    {},
	"$.usage.output_bytes":                   {},
	"$.usage.image_pixels":                   {},
	"$.usage.video_frames":                   {},
	"$.usage.file_pages":                     {},
	"$.usage.bytes":                          {},
	"$.usage.queries":                        {},
	"$.usage.credits":                        {},
	"$.usage.duration_seconds":               {},
	"$.usage.frame_count":                    {},
	"$.usage.page_count":                     {},
	"$.billing.amount":                       {},
	"$.billing.currency":                     {},
	"$.charge.amount":                        {},
	"$.charge.currency":                      {},
	"$.charge.id":                            {},
	"$.charge.kind":                          {},
	"$.cost.amount":                          {},
	"$.cost.currency":                        {},
	"$.price.amount":                         {},
	"$.price.currency":                       {},
	"$.quota.limit":                          {},
	"$.quota.remaining":                      {},
	"$.quota.reset":                          {},
	"$.allowance.limit":                      {},
	"$.allowance.remaining":                  {},
	"$.allowance.reset":                      {},
	"$.rate_limit.limit":                     {},
	"$.rate_limit.remaining":                 {},
	"$.rate_limit.reset":                     {},
	"$.cache.read_tokens":                    {},
	"$.cache.write_tokens":                   {},
	"$.cache.lifetime":                       {},
	"$.provider_schema.custom_cost":          {},
	"$.provider_schema.service_context":      {},
	"$.provider_schema.service_tier":         {},
}

var safeEvidenceHeaderAllowlist = map[string]struct{}{
	"content-length":                   {},
	"x-request-id":                     {},
	"x-correlation-id":                 {},
	"x-provider-request-id":            {},
	"x-provider-charge-id":             {},
	"x-provider-account-id":            {},
	"x-provider-account-key":           {},
	"x-usage-input-tokens":             {},
	"x-usage-output-tokens":            {},
	"x-usage-total-tokens":             {},
	"x-usage-cache-read-input-tokens":  {},
	"x-usage-cache-write-input-tokens": {},
	"x-usage-reasoning-output-tokens":  {},
	"x-usage-audio-seconds":            {},
	"x-usage-video-seconds":            {},
	"x-usage-image-count":              {},
	"x-usage-file-pages":               {},
	"x-usage-queries":                  {},
	"x-usage-credits":                  {},
	"x-usage-bytes":                    {},
	"x-usage-duration-seconds":         {},
	"x-usage-frame-count":              {},
	"x-billing-amount":                 {},
	"x-billing-currency":               {},
	"x-charge-amount":                  {},
	"x-charge-currency":                {},
	"x-charge-item-id":                 {},
	"x-charge-kind":                    {},
	"x-cost-amount":                    {},
	"x-cost-currency":                  {},
	"x-price-amount":                   {},
	"x-price-currency":                 {},
	"x-quota-limit":                    {},
	"x-quota-remaining":                {},
	"x-quota-reset":                    {},
	"x-allowance-limit":                {},
	"x-allowance-remaining":            {},
	"x-allowance-reset":                {},
	"x-rate-limit-limit":               {},
	"x-rate-limit-remaining":           {},
	"x-rate-limit-reset":               {},
	"x-ratelimit-limit":                {},
	"x-ratelimit-remaining":            {},
	"x-ratelimit-reset":                {},
	"x-cache-read-tokens":              {},
	"x-cache-write-tokens":             {},
	"x-cache-lifetime":                 {},
	"x-provider-usage-input-tokens":    {},
	"x-provider-usage-output-tokens":   {},
	"x-provider-usage-total-tokens":    {},
	"x-provider-billing-amount":        {},
	"x-provider-billing-currency":      {},
	"x-provider-charge-amount":         {},
	"x-provider-charge-currency":       {},
}

func validateSafeEvidenceLocation(location string) error {
	if !utf8.ValidString(location) {
		return fmt.Errorf("safe evidence location must be valid UTF-8")
	}
	if len(location) > MaxSafeEvidenceFieldBytes {
		return fmt.Errorf("safe evidence location exceeds %d bytes", MaxSafeEvidenceFieldBytes)
	}
	if len(location) > 0 && location[0] == '$' {
		if _, ok := safeEvidencePathAllowlist[location]; !ok {
			return fmt.Errorf("safe evidence path %q is not an allowlisted economic location", location)
		}
		return nil
	}
	if _, ok := safeEvidenceHeaderAllowlist[strings.ToLower(location)]; !ok {
		return fmt.Errorf("safe evidence header %q is not an allowlisted economic location", location)
	}
	return nil
}

func validateSafeEvidenceLexeme(field, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	return validateBoundedText(field, value, MaxSafeEvidenceFieldBytes)
}

func isKnownSafeEvidenceAcquisition(value string) bool {
	_, ok := knownAcquisitionOrigin[value]
	return ok
}

func safeEvidenceLocationKey(field SafeEvidenceField) string {
	location := field.Path
	if location == "" {
		location = field.Name
	}
	if location == "" || location[0] != '$' {
		location = strings.ToLower(location)
	}
	return location
}
