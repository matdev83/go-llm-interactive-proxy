# 2026-07-24 - [Fix error message leak in JSON parsing during audit redaction]

**Vulnerability:** A malformed raw event payload failing JSON parsing in `redactAuditResultJSON` exposed the entire payload because it fell back to returning `raw` instead of a masked event digest.
**Learning:** `json.Unmarshal` failures must not leak untrusted input strings back into the parsed outcome structure, as this circumvents the `best_effort` redaction policy.
**Prevention:** Always fallback to wrapping `DigestJSONFields(raw, pol)` in an `event_digest` object on unmarshal failures.

## 2026-07-25 - [Fix timing attack in bearer token comparison]

**Vulnerability:** Comparing bearer tokens with `subtle.ConstantTimeCompare` without hashing them first leaks the expected length of the token due to early returns on mismatched slice lengths.
**Learning:** `subtle.ConstantTimeCompare` only provides constant time properties when both byte slices are the same length. Comparing strings of variable lengths will return early, causing a side channel leak of the exact length of the expected token.
**Prevention:** Hash the expected and actual tokens with a uniform hash like `sha256.Sum256` before comparing them. This ensures both values passed to `ConstantTimeCompare` are exactly the same length.
