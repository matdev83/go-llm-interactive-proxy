package workstore

import (
	"errors"

	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

var (
	ErrIdentityCollision    = errors.New("terminalwork/workstore: identity collision")
	ErrNotFound             = errors.New("terminalwork/workstore: not found")
	ErrConflict             = sdk.ErrConflict
	ErrQueryTooBroad        = errors.New("terminalwork/workstore: query too broad")
	ErrQueryLimitExceeded   = errors.New("terminalwork/workstore: query limit exceeded")
	ErrUniqueRaceMissingRow = errors.New("terminalwork/workstore: unique race missing winner row")
)

const MaxQueryLimit = 500
