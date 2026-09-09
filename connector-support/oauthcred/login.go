package oauthcred

import (
	"fmt"
	"os"
	"strings"
)

// LoginModePreProvisionedRefreshOnly marks connectors whose initial
// browser/device authorization is intentionally NOT implemented in Go-LIP.
//
// Such connectors consume credentials provisioned out-of-band through the
// vendor's official authorization channels and implement only the refresh
// half of the OAuth lifecycle (proactive refresh, terminal quarantine,
// explicit re-login). Reimplementing a vendor's first-party login flow
// inside the connector would be private-interface scraping, so Configure
// fails closed with an actionable error until a credential exists.
const LoginModePreProvisionedRefreshOnly = "pre-provisioned-refresh-only"

// RequireCredential loads the stored OAuth record and fails closed with an
// actionable operator error when no usable credential exists.
//
// loginHint must tell the operator how to obtain a credential (for example,
// which login flow to run first) without revealing secrets. The returned
// error never contains token material: only the provider name, the store
// path, and the caller-supplied hint.
//
// A record holding only a refresh token is accepted: that is the normal
// pre-provisioned state and the refresh path will mint access tokens.
func RequireCredential(store Store, provider, loginHint string) (TokenRecord, error) {
	if store == nil {
		return TokenRecord{}, fmt.Errorf("%s: no OAuth credential store configured; %s", provider, loginHint)
	}
	rec, err := store.Load()
	if err != nil {
		if os.IsNotExist(err) {
			return TokenRecord{}, fmt.Errorf("%s: no OAuth credential at %q; %s", provider, store.Path(), loginHint)
		}
		return TokenRecord{}, fmt.Errorf("%s: load OAuth credential: %w", provider, err)
	}
	if rec.Quarantined {
		reason := strings.TrimSpace(rec.QuarantineReason)
		if reason == "" {
			reason = "credentials quarantined after terminal refresh failure"
		}
		return TokenRecord{}, fmt.Errorf("%s: OAuth credential is quarantined (%s); %s", provider, reason, loginHint)
	}
	if strings.TrimSpace(rec.AccessToken) == "" && strings.TrimSpace(rec.RefreshToken) == "" {
		return TokenRecord{}, fmt.Errorf("%s: OAuth credential at %q holds no tokens; %s", provider, store.Path(), loginHint)
	}
	return rec, nil
}
