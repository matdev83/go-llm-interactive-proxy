package product

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClosePoolThenBridge_OrderAndJoin(t *testing.T) {
	t.Parallel()
	errPool := errors.New("pool boom")
	errBridge := errors.New("bridge boom")
	var order []string
	err := closePoolThenBridge(
		func() error {
			order = append(order, "pool")
			return errPool
		},
		func() error {
			order = append(order, "bridge")
			return errBridge
		},
	)
	require.Equal(t, []string{"pool", "bridge"}, order)
	require.ErrorIs(t, err, errPool)
	require.ErrorIs(t, err, errBridge)
}

func TestClosePoolThenBridge_IdempotentNilClosers(t *testing.T) {
	t.Parallel()
	require.NoError(t, closePoolThenBridge(nil, nil))
	var n atomic.Int32
	once := func() error {
		n.Add(1)
		return nil
	}
	require.NoError(t, closePoolThenBridge(once, once))
	require.EqualValues(t, 2, n.Load())
}
