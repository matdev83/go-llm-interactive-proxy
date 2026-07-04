package db

import (
	"fmt"
	"net/url"
	"strings"
)

// pgUnsupportedDSNParams lists libpq connection parameters that Bun pgdriver
// does not honor. pgdriver forwards unknown URL query parameters to the server
// as SET commands; these options are not server GUCs, so the connection fails
// with an opaque driver error. Neon and other managed providers emit
// channel_binding=require by default, so stripping it here lets operators paste
// a libpq DSN directly into config without manual editing. TLS behavior stays
// governed by sslmode.
var pgUnsupportedDSNParams = []string{"channel_binding"}

// SanitizePostgresDSN returns dsn with libpq-only query parameters that Bun
// pgdriver cannot apply removed. Non-URL DSNs are returned unchanged (pgdriver
// only accepts URL-style DSNs, so a non-URL DSN will fail later with a clear
// "invalid scheme" error). The function does not log or expose DSN secrets.
func SanitizePostgresDSN(dsn string) (string, error) {
	before, after, ok := strings.Cut(dsn, "?")
	if !ok {
		return dsn, nil
	}
	base, rawQuery := before, after
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("db: parse postgres dsn query: %w", err)
	}
	removed := false
	for _, target := range pgUnsupportedDSNParams {
		for k := range vals {
			if strings.EqualFold(k, target) {
				vals.Del(k)
				removed = true
			}
		}
	}
	if !removed {
		return dsn, nil
	}
	encoded := vals.Encode()
	if encoded == "" {
		return base, nil
	}
	return base + "?" + encoded, nil
}
