package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/oauthcred"
)

// LoginSession is one in-progress MiniMax PKCE device authorization.
//
// It is created by StartLogin (which performs POST /oauth/code and returns
// the displayable verification URI + user code) and finished with Complete
// (which polls POST /oauth/token until approval and persists the minted
// credential via the existing oauthcred store). The session holds the PKCE
// code verifier, which never leaves the process except inside the token
// poll request body.
type LoginSession struct {
	// UserCode is the short code the operator types at the verification URI.
	UserCode string
	// VerificationURI is the browser page where the operator approves access.
	VerificationURI string
	// ExpiresIn bounds how long the user code stays valid (seconds or unix millis, as returned by the portal).
	ExpiresIn int64
	// Interval hints the poll cadence in milliseconds (PollToken floors it at 2s).
	Interval int

	portalBaseURL string
	clientID      string
	scope         string
	codeVerifier  string
	state         string
	httpClient    *http.Client
}

// StartLogin begins the MiniMax PKCE device flow: it generates a fresh PKCE
// verifier/challenge pair plus random state, calls POST /oauth/code, and
// returns the verification details the operator must act on. No credential
// is persisted yet; call Complete after the operator approves access.
func StartLogin(ctx context.Context, client *http.Client, portalBaseURL, clientID, scope string) (*LoginSession, error) {
	if client == nil {
		client = http.DefaultClient
	}
	portal := strings.TrimRight(strings.TrimSpace(portalBaseURL), "/")
	if portal == "" {
		return nil, errors.New("minimax-oauth: portal base URL is required to start login")
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return nil, errors.New("minimax-oauth: oauth_client_id is required to start login (Hermes client_id must not be hardcoded)")
	}
	if strings.TrimSpace(scope) == "" {
		scope = DefaultScope
	}

	verifier, challenge, state, err := oauthcred.GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("minimax-oauth: start login: %w", err)
	}
	resp, err := RequestUserCode(ctx, client, portal, clientID, scope, challenge, state)
	if err != nil {
		return nil, fmt.Errorf("minimax-oauth: start login: %w", err)
	}

	return &LoginSession{
		UserCode:        resp.UserCode,
		VerificationURI: resp.VerificationURI,
		ExpiresIn:       resp.ExpiredIn,
		Interval:        resp.Interval,
		portalBaseURL:   portal,
		clientID:        clientID,
		scope:           scope,
		codeVerifier:    verifier,
		state:           state,
		httpClient:      client,
	}, nil
}

// Instructions returns the operator-facing approval steps. The user code and
// verification URI are display values by design; no token material is included.
func (s *LoginSession) Instructions() string {
	if s == nil {
		return "minimax-oauth: no login session"
	}
	return fmt.Sprintf("minimax-oauth login: open %s and enter code %s, then wait for approval", s.VerificationURI, s.UserCode)
}

// Complete polls POST /oauth/token until the operator approves (or the user
// code expires / ctx is cancelled) and persists the minted credential via
// store. Persisting a fresh record clears any previous quarantine, and the
// refresh path afterwards is unchanged.
func (s *LoginSession) Complete(ctx context.Context, store oauthcred.Store) (oauthcred.TokenRecord, error) {
	if s == nil {
		return oauthcred.TokenRecord{}, errors.New("minimax-oauth: no login session to complete")
	}
	if store == nil {
		return oauthcred.TokenRecord{}, errors.New("minimax-oauth: credential store is required to complete login")
	}
	rec, err := PollToken(ctx, s.httpClient, s.portalBaseURL, s.clientID, s.UserCode, s.codeVerifier, s.ExpiresIn, s.Interval)
	if err != nil {
		return oauthcred.TokenRecord{}, fmt.Errorf("minimax-oauth: complete login: %w", err)
	}
	if err := store.Save(rec); err != nil {
		return oauthcred.TokenRecord{}, fmt.Errorf("minimax-oauth: persist login credential: %w", err)
	}
	return rec, nil
}
