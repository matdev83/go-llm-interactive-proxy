package openaicodex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshOAuthAccessTokenReturnsUpdatedConfigWithoutMutatingInput(t *testing.T) {
	t.Parallel()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
		})
	}))
	t.Cleanup(tokenSrv.Close)

	cfg := Config{
		AccessToken:   "old-access",
		RefreshToken:  "old-refresh",
		OAuthTokenURL: tokenSrv.URL,
	}
	refreshed, err := refreshOAuthAccessToken(context.Background(), cfg, tokenSrv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessToken != "old-access" || cfg.RefreshToken != "old-refresh" {
		t.Fatalf("input config mutated: %+v", cfg)
	}
	if refreshed.AccessToken != "new-access" || refreshed.RefreshToken != "new-refresh" {
		t.Fatalf("refreshed config: %+v", refreshed)
	}
}
