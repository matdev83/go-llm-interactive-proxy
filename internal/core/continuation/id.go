package continuation

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// NewResponseID returns a high-entropy proxy response ID.
func NewResponseID(ctx context.Context) (lipcont.ResponseID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var buf [lipcont.MinResponseIDEntropyBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("continuation: response id entropy: %w", err)
	}
	return lipcont.ResponseID(lipcont.ResponseIDPrefix + base64.RawURLEncoding.EncodeToString(buf[:])), nil
}
