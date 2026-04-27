package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"

	sdkauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

// LocalAPIKeyAuthenticator validates bearer API keys against operator-configured records.
// Records must pass [ValidateLocalAPIKeyRecords] before construction.
type LocalAPIKeyAuthenticator struct {
	records []localAPIKeyEntry
}

type localAPIKeyEntry struct {
	keyID        string
	principalID  string
	secretDigest [sha256.Size]byte
	fingerprint  string
}

// NewLocalAPIKeyAuthenticator builds an authenticator from validated key records.
func NewLocalAPIKeyAuthenticator(records []LocalAPIKeyRecord) (*LocalAPIKeyAuthenticator, error) {
	if err := ValidateLocalAPIKeyRecords(records); err != nil {
		return nil, err
	}
	out := make([]localAPIKeyEntry, 0, len(records))
	for _, r := range records {
		kid := strings.TrimSpace(r.KeyID)
		pid := strings.TrimSpace(r.PrincipalID)
		sec := strings.TrimSpace(r.Key)
		out = append(out, localAPIKeyEntry{
			keyID:        kid,
			principalID:  pid,
			secretDigest: sha256.Sum256([]byte(sec)),
			fingerprint:  redactedAPIKeyFingerprint(kid, sec),
		})
	}
	return &LocalAPIKeyAuthenticator{records: out}, nil
}

// redactedAPIKeyFingerprint derives a stable, non-reversible-at-a-glance fingerprint using
// HMAC-SHA256 keyed by non-secret key_id over the secret (design: fingerprinting, not storage).
func redactedAPIKeyFingerprint(keyID, secret string) string {
	mac := hmac.New(sha256.New, []byte(keyID))
	_, _ = mac.Write([]byte(secret))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:12])
}

// Authenticate implements [Authenticator].
func (a *LocalAPIKeyAuthenticator) Authenticate(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	_ = ctx
	if a == nil || len(a.records) == 0 {
		return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "api_key_misconfigured"}, nil
	}
	raw := stripBearer(strings.TrimSpace(req.AuthorizationBearer))
	if len(raw) == 0 {
		return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "missing_api_key"}, nil
	}
	presentedDigest := sha256.Sum256([]byte(raw))
	matched := -1
	for i, r := range a.records {
		if subtle.ConstantTimeCompare(presentedDigest[:], r.secretDigest[:]) == 1 {
			matched = i
		}
	}
	if matched >= 0 {
		r := a.records[matched]
		return sdkauth.Decision{
			Outcome: sdkauth.OutcomeAllow,
			Principal: execview.PrincipalView{
				ID: strings.TrimSpace(r.principalID),
			},
			Device: sdkauth.DeviceIdentity{
				ID:          r.principalID + ":" + r.keyID,
				KeyID:       r.keyID,
				Fingerprint: r.fingerprint,
			},
			SatisfiedLevel: sdkauth.LevelAPIKey,
		}, nil
	}
	return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "unknown_api_key"}, nil
}

func stripBearer(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 7 && strings.EqualFold(s[:7], "bearer ") {
		return strings.TrimSpace(s[7:])
	}
	return s
}
