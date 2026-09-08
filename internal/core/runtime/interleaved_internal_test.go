package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
)

// TestInterleavedPhaseForRole_MapsEveryRole covers interleavedPhaseForRole for every Role value
// so a new role added without updating the switch surfaces as an empty phase rather than a silent
// mislabeling.
func TestInterleavedPhaseForRole_MapsEveryRole(t *testing.T) {
	t.Parallel()
	cases := []struct {
		role interleavedstate.Role
		want string
	}{
		{interleavedstate.RoleThinker, "thinker"},
		{interleavedstate.RoleExecutor, "executor"},
		{interleavedstate.RoleNone, ""},
		{"unknown-role", ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			t.Parallel()
			if got := interleavedPhaseForRole(tc.role); got != tc.want {
				t.Fatalf("interleavedPhaseForRole(%q) = %q want %q", tc.role, got, tc.want)
			}
		})
	}
}

func TestInterleavedContinuationStream_UnknownPhaseRecvError(t *testing.T) {
	t.Parallel()
	s := &interleavedContinuationStream{}
	_, err := s.Recv(context.Background())
	if !errors.Is(err, errUnknownInterleavedPhase) {
		t.Fatalf("got %v", err)
	}
}
