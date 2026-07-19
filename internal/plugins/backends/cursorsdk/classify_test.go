package cursorsdk

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyFailure_Table(t *testing.T) {
	t.Parallel()
	secret := "secret-api-key-value"
	cases := []struct {
		name        string
		err         error
		committed   bool
		code        FailureCode
		recoverable bool
		phase       lipapi.OutputPhase
		forbid      []string
		root        error
	}{
		{
			name: "missing_executable",
			err:  NewBridgeFault(CodeBridgeMissing, fmt.Errorf("start bridge: %w", os.ErrNotExist), ""),
			code: CodeBridgeMissing, recoverable: false, phase: lipapi.PhasePreOutput,
			root: os.ErrNotExist,
		},
		{
			name: "empty_executable",
			err:  NewBridgeFault(CodeBridgeMissing, ErrBridgeMissing, "bridge executable is empty"),
			code: CodeBridgeMissing, recoverable: false, phase: lipapi.PhasePreOutput,
			root: ErrBridgeMissing,
		},
		{
			name: "incompatible_schema",
			err:  NewBridgeFault(CodeBridgeIncompatible, ErrBridgeIncompatible, "schemaVersion 2 != 1"),
			code: CodeBridgeIncompatible, recoverable: false, phase: lipapi.PhasePreOutput,
			root: ErrBridgeIncompatible,
		},
		{
			name: "protocol_error_body_code",
			err:  NewBridgeFault(CodeBridgeProtocol, &protocol.ProtocolError{Class: protocol.ErrorSequenceRegression, Message: "seq"}, ""),
			code: CodeBridgeProtocol, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "auth_failed",
			err:  errors.New("agent/create: unauthorized: invalid_api_key " + secret),
			code: CodeAuthFailed, recoverable: false, phase: lipapi.PhasePreOutput,
			forbid: []string{secret},
		},
		{
			name: "unknown_model",
			err:  errors.New("cursor_sdk_model_unknown: model not found"),
			code: CodeModelUnknown, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "agent_busy",
			err:  ErrAgentBusy,
			code: CodeAgentBusy, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "agent_limit",
			err:  ErrAgentLimit,
			code: CodeAgentLimit, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "run_limit",
			err:  ErrRunLimit,
			code: CodeAgentLimit, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "protocol_sequence",
			err:  NewBridgeFault(CodeBridgeProtocol, &protocol.ProtocolError{Class: protocol.ErrorSequenceRegression}, ""),
			code: CodeBridgeProtocol, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "protocol_oversize",
			err:  NewBridgeFault(CodeBridgeProtocol, &protocol.ProtocolError{Class: protocol.ErrorFrameTooLarge}, ""),
			code: CodeBridgeProtocol, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "cancel_timeout",
			err:  errCancelTimeout,
			code: CodeCancelTimeout, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "process_exit_pre_output",
			err:  BridgeExited(nil, ""),
			code: CodeBridgeExited, recoverable: true, phase: lipapi.PhasePreOutput,
			root: ErrBridgeExited,
		},
		{
			name:      "process_exit_post_output",
			err:       BridgeExited(nil, ""),
			committed: true,
			code:      CodeBridgeExited, recoverable: false, phase: lipapi.PhasePostOutput,
			root: ErrBridgeExited,
		},
		{
			name: "start_failed_pre_output",
			err:  BridgeStartTransient(errors.New("connection reset"), ""),
			code: CodeBridgeStartFailed, recoverable: true, phase: lipapi.PhasePreOutput,
			root: ErrBridgeStartFailed,
		},
		{
			name: "path_redacted",
			err: BridgeStartTransient(errors.New("spawn failed"),
				`/home/user/proj/bridge and C:\Users\x\a.exe prompt: secretstuff`),
			code: CodeBridgeStartFailed, recoverable: true, phase: lipapi.PhasePreOutput,
			forbid: []string{"/home/user", `C:\Users`, "secretstuff"},
			root:   ErrBridgeStartFailed,
		},
		{
			name: "stderr_auth_does_not_poison_exit",
			err:  BridgeExited(errors.New("exit status 1"), "unauthorized invalid_api_key "+secret+" incompatible unsupported"),
			code: CodeBridgeExited, recoverable: true, phase: lipapi.PhasePreOutput,
			forbid: []string{secret},
			root:   ErrBridgeExited,
		},
		{
			name: "stderr_incompatible_does_not_poison_exit",
			err:  BridgeExited(nil, "incompatible_version schemaVersion boom"),
			code: CodeBridgeExited, recoverable: true, phase: lipapi.PhasePreOutput,
			root: ErrBridgeExited,
		},
		{
			name: "author_not_auth",
			err:  errors.New("cursorsdk: author metadata invalid"),
			code: CodeRunFailed, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "oauth_not_auth",
			err:  errors.New("cursorsdk: oauth token refresh skipped"),
			code: CodeRunFailed, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "path_with_auth_segment_not_auth",
			err:  errors.New(`read failed at /home/user/auth/keys/id`),
			code: CodeRunFailed, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "disk_full_not_recoverable",
			err:  errors.New("cursorsdk: bridge write failed: disk full"),
			code: CodeRunFailed, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "start_failed_unsupported_platform_typed",
			err:  BridgeStartTransient(errors.New("unsupported platform"), ""),
			code: CodeBridgeStartFailed, recoverable: true, phase: lipapi.PhasePreOutput,
			root: ErrBridgeStartFailed,
		},
		{
			name: "capability_exact_code",
			err:  errors.New("cursor_sdk_capability_unsupported: tools"),
			code: CodeCapabilityUnsupported, recoverable: false, phase: lipapi.PhasePreOutput,
		},
		{
			name: "bare_unsupported_not_capability",
			err:  errors.New("cursorsdk: feature unsupported in this build"),
			code: CodeRunFailed, recoverable: false, phase: lipapi.PhasePreOutput,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ClassifyFailure(tc.err, tc.committed, secret)
			require.NotNil(t, got)
			assert.Equal(t, tc.code, got.Code)
			assert.Equal(t, tc.recoverable, got.Recoverable)
			assert.Equal(t, tc.phase, got.Phase)
			for _, bad := range tc.forbid {
				assert.NotContains(t, got.Error(), bad)
			}
			if tc.root != nil {
				assert.ErrorIs(t, got, tc.root)
			}
			wrapped := MapOrchestrationError(got)
			assert.Equal(t, tc.recoverable && tc.phase == lipapi.PhasePreOutput, lipapi.IsRecoverablePreOutput(wrapped))
			if tc.recoverable && tc.phase == lipapi.PhasePreOutput {
				assert.ErrorIs(t, wrapped, tc.root)
			}
		})
	}
}

func TestClassifyFailure_Nil(t *testing.T) {
	t.Parallel()
	assert.Nil(t, ClassifyFailure(nil, false, ""))
	assert.Nil(t, MapOrchestrationError(nil))
}

func TestClassifyFailure_ProtocolErrorAs(t *testing.T) {
	t.Parallel()
	err := &protocol.ProtocolError{Class: protocol.ErrorInvalidJSON, Message: "bad"}
	got := ClassifyFailure(err, false, "")
	require.NotNil(t, got)
	assert.Equal(t, CodeBridgeProtocol, got.Code)
	assert.False(t, got.Recoverable)
}
