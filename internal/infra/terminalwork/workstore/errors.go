package workstore

import (
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

var (
	ErrIdentityCollision    = errors.New("terminalwork/workstore: identity collision")
	ErrNotFound             = errors.New("terminalwork/workstore: not found")
	ErrConflict             = sdk.ErrConflict
	ErrQueryTooBroad        = terminalwork.ErrQueryTooBroad
	ErrQueryLimitExceeded   = terminalwork.ErrQueryLimitExceeded
	ErrUniqueRaceMissingRow = errors.New("terminalwork/workstore: unique race missing winner row")
)

const MaxQueryLimit = terminalwork.MaxQueryLimit
