package product

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProcessIdentityParityContract(t *testing.T) {
	ins := fakeInspector()
	p := newFakeProc(970001)
	id := ins.capture(p, "/opt/lip-cursor-sdk-bridge")
	require.Equal(t, 970001, id.PID)
	require.False(t, id.CreateTime.IsZero())
	require.NotEmpty(t, id.ExeKey)
	require.True(t, ins.stillSame(p, id))
	id.ExeKey = normalizeExeKey("/other/exe")
	require.False(t, ins.stillSame(p, id))
}
