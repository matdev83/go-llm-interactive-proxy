package archtest

// Negative fixtures for the ALG feature-side ownership ratchets: the direct
// client/A-leg append (design ratchet 4), the second terminal owner
// (design ratchet 5), the retired hidden-content owner (design ratchet 5) and the
// second policy endpoint/store (design ratchet 6).
//
// As in the sibling fixture files, each test asserts the validator's verdict on a
// deliberately violating miniature feature package, not merely that a sample
// parses. See agent_loop_guard_ownership_fixture_test.go for the shared helpers
// and agent_loop_guard_terminal_owner_census.go for the census under test.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTask121FixtureALGRejectsClientCallAppend proves design ratchet 4 fires on
// a direct append into the canonical client/A-leg call, and that appending to an
// unrelated slice is not confused with it.
func TestTask121FixtureALGRejectsClientCallAppend(t *testing.T) {
	t.Parallel()
	t.Run("positive canonical writer usage passes", func(t *testing.T) {
		t.Parallel()
		src := `package agentloopguard

func release(ctx context.Context, w Writer, msg lipapi.Item) error {
	return w.AppendVisible(ctx, msg)
}
`
		assert.Empty(t, scanAlgOwnershipSyntheticSource(t, algFeatureRootDir+"/completiontool.go", src))
	})

	for _, tc := range []struct {
		name    string
		relPath string
		src     string
		want    string
	}{
		{
			name:    "appends to client call messages",
			relPath: algFeatureRootDir + "/completiontool.go",
			src:     "package agentloopguard\n\nfunc inject(call *lipapi.Call, msg lipapi.Item) {\n\tcall.Messages = append(call.Messages, msg)\n}\n",
			want:    `field "Messages"`,
		},
		{
			name:    "appends through a request struct field",
			relPath: algFeatureRootDir + "/completiontool_handle.go",
			src:     "package agentloopguard\n\nfunc inject(in *lipapi.Call, msg lipapi.Item) {\n\tin.Items = append(in.Items, msg)\n}\n",
			want:    `field "Items"`,
		},
		{
			name:    "appends to an a-leg aliased call",
			relPath: algFeatureRootDir + "/provider.go",
			src:     "package agentloopguard\n\nfunc inject(c lipapi.Call, msg lipapi.Item) lipapi.Call {\n\tc.Messages = append(c.Messages, msg)\n\treturn c\n}\n",
			want:    `field "Messages"`,
		},
		{
			name:    "appends through an aliased messages slice",
			relPath: algFeatureRootDir + "/completiontool.go",
			src:     "package agentloopguard\n\nfunc inject(c lipapi.Call, msg lipapi.Item) lipapi.Call {\n\tmsgs := c.Messages\n\tmsgs = append(msgs, msg)\n\tc.Messages = msgs\n\treturn c\n}\n",
			want:    `field "Messages"`,
		},
		{
			name:    "appends additively through the field selector",
			relPath: algFeatureRootDir + "/completiontool.go",
			src:     "package agentloopguard\n\nfunc inject(c lipapi.Call, msg lipapi.Item) lipapi.Call {\n\tc.Items += append(c.Items, msg)\n\treturn c\n}\n",
			want:    `field "Items"`,
		},
		{
			name:    "appends through a var-declared alias",
			relPath: algFeatureRootDir + "/completiontool_handle.go",
			src:     "package agentloopguard\n\nfunc inject(c lipapi.Call, msg lipapi.Item) lipapi.Call {\n\tvar items = c.Items\n\titems = append(items, msg)\n\tc.Items = items\n\treturn c\n}\n",
			want:    `field "Items"`,
		},
		{
			name:    "appends through an alias inside a closure",
			relPath: algFeatureRootDir + "/completiontool.go",
			src:     "package agentloopguard\n\nfunc inject(c lipapi.Call, msg lipapi.Item) lipapi.Call {\n\twith := func() {\n\t\tmsgs := c.Messages\n\t\tmsgs = append(msgs, msg)\n\t}\n\twith()\n\treturn c\n}\n",
			want:    `field "Messages"`,
		},
		{
			// Requirement 3.1 forbids the feature from writing ANY canonical
			// A-leg truth, not only the transcript slices: Instructions and
			// Tools are the trajectory and the client tool catalog, and the
			// approved generic owner (pkg/lipsdk/controltool.Project) mutates
			// exactly those three fields. Without them in the field set, the
			// ratchet could not see a feature-side rewrite of either.
			name:    "appends to the client instruction trajectory",
			relPath: algFeatureRootDir + "/completiontool.go",
			src:     "package agentloopguard\n\nfunc inject(c lipapi.Call, msg lipapi.Message) lipapi.Call {\n\tc.Instructions = append(c.Instructions, msg)\n\treturn c\n}\n",
			want:    `field "Instructions"`,
		},
		{
			name:    "appends to the client tool catalog",
			relPath: algFeatureRootDir + "/completiontool_handle.go",
			src:     "package agentloopguard\n\nfunc inject(c lipapi.Call, tool lipapi.ToolDef) lipapi.Call {\n\tc.Tools = append(c.Tools, tool)\n\treturn c\n}\n",
			want:    `field "Tools"`,
		},
		{
			// The same mutation in the APPROVED generic owner stays legal: the
			// ratchet binds the feature, not the SDK contract that exists to
			// perform this projection on the feature's behalf.
			name:    "the approved generic SDK owner performs this projection legally",
			relPath: algControlToolSDK + "/projection.go",
			src:     "package controltool\n\nfunc project(out lipapi.Call, instruction Instruction, tool lipapi.ToolDef) lipapi.Call {\n\tout.Items = prepend([]lipapi.Item{controlItem(instruction)}, out.Items)\n\tout.Instructions = prepend([]lipapi.Message{controlMessage(instruction)}, out.Instructions)\n\tout.Tools = append(out.Tools, tool)\n\treturn out\n}\n",
			want:    "",
		},
		{
			name:    "appends to an unrelated slice",
			relPath: algFeatureRootDir + "/completiontool.go",
			src:     "package agentloopguard\n\nfunc grow(lines []string) []string {\n\tlines = append(lines, \"note\")\n\treturn lines\n}\n",
			want:    "",
		},
		{
			name:    "an identically named local in another function is not an alias",
			relPath: algFeatureRootDir + "/completiontool.go",
			src:     "package agentloopguard\n\nfunc one(msgs []lipapi.Item, msg lipapi.Item) []lipapi.Item {\n\tmsgs = append(msgs, msg)\n\treturn msgs\n}\n\nfunc two(msgs []lipapi.Item, msg lipapi.Item) []lipapi.Item {\n\tmsgs = append(msgs, msg)\n\treturn msgs\n}\n",
			want:    "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := scanAlgOwnershipSyntheticSource(t, tc.relPath, tc.src)
			if tc.want == "" {
				assert.Empty(t, findings, algFixtureRender(findings))
				return
			}
			require.NotEmpty(t, findings, algFixtureRender(findings))
			assert.True(t, algFixtureHasFinding(findings, RuleALGClientCallAppend, tc.want),
				"client-append ratchet must reject this violation for its own reason:\n%s", algFixtureRender(findings))
		})
	}
}

// algCensusProviderFile is a miniature approved legacy receiver file.
const algCensusProviderFile = `package agentloopguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

func NewConfiguredProvider(cfg Config) (terminaldecision.Provider, error) {
	switch cfg.Strategy {
	case StrategySemanticVerifier:
		return provider{cfg: cfg}, nil
	case StrategyAttemptCompletion:
		return preferredProvider{}, nil
	default:
		return nil, nil
	}
}

type provider struct{ cfg Config }

func (provider) ID() string { return providerID }

func (p provider) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	return allowStop(reasonUnfinished), nil
}

func allowStop(reason string) terminaldecision.Decision { return terminaldecision.Decision{} }
`

// algCensusPreferredFile is a miniature approved preferred receiver file.
const algCensusPreferredFile = `package agentloopguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

type preferredProvider struct{}

func (preferredProvider) ID() string { return providerID }

func (p preferredProvider) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	return allowStop(reasonDeadline), nil
}

func (p preferredProvider) limits() int { return p.maxProtocolReprompts() }
`

// TestTask121FixtureALGRejectsSecondTerminalOwner proves design ratchet 5 /
// Requirement 9.5 fires on a third terminal-policy receiver, on an approved
// receiver relocated to the wrong file, and on a missing approved receiver,
// while the approved two-receiver census passes.
func TestTask121FixtureALGRejectsSecondTerminalOwner(t *testing.T) {
	t.Parallel()
	t.Run("positive approved census passes", func(t *testing.T) {
		t.Parallel()
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    algCensusProviderFile,
			algPreferredProviderFile: algCensusPreferredFile,
		})
		assert.Empty(t, algFixtureRender(scanAlgTerminalOwnerCensus(files)))
	})

	t.Run("third terminal receiver is a second terminal owner", func(t *testing.T) {
		t.Parallel()
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    algCensusProviderFile,
			algPreferredProviderFile: algCensusPreferredFile,
			algFeatureRootDir + "/recovery_owner.go": `package agentloopguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

type recoveryOwner struct{}

func (recoveryOwner) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	return allowStop(reasonUnfinished), nil
}
`,
		})
		findings := scanAlgTerminalOwnerCensus(files)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGSecondTerminalOwner,
			`unapproved terminal-policy receiver "recoveryOwner"`),
			"terminal-owner census must reject a third receiver:\n%s", algFixtureRender(findings))
	})

	t.Run("approved receiver relocated to another file", func(t *testing.T) {
		t.Parallel()
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:                              algCensusProviderFile,
			algFeatureRootDir + "/preferred_provider_moved.go": algCensusPreferredFile,
		})
		findings := scanAlgTerminalOwnerCensus(files)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGSecondTerminalOwner,
			`is implemented in internal/plugins/features/agentloopguard/preferred_provider_moved.go, want internal/plugins/features/agentloopguard/preferred_provider.go`),
			"terminal-owner census must pin each receiver to its approved file:\n%s", algFixtureRender(findings))
	})

	t.Run("missing approved receiver is an incomplete census", func(t *testing.T) {
		t.Parallel()
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile: algCensusProviderFile,
		})
		findings := scanAlgTerminalOwnerCensus(files)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGSecondTerminalOwner,
			`approved ALG terminal receiver "preferredProvider" is absent`),
			"terminal-owner census must fail closed when an approved receiver disappears:\n%s", algFixtureRender(findings))
	})

	t.Run("second terminal owner reached by embedding is counted", func(t *testing.T) {
		t.Parallel()
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    algCensusProviderFile,
			algPreferredProviderFile: algCensusPreferredFile,
			algFeatureRootDir + "/recovery_owner.go": `package agentloopguard

type recoveryOwner struct{ provider }

func (recoveryOwner) publish(ctx context.Context) {}
`,
		})
		findings := scanAlgTerminalOwnerCensus(files)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGSecondTerminalOwner,
			`unapproved terminal-policy receiver "recoveryOwner"`),
			"a receiver that inherits Decide through an embedded approved receiver is a second terminal owner:\n%s", algFixtureRender(findings))
	})

	t.Run("non-seam Decide on an approved receiver is refused", func(t *testing.T) {
		t.Parallel()
		nonSeam := strings.Replace(algCensusPreferredFile,
			"Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error)",
			"Decide(n int) int", 1)
		require.NotEqual(t, algCensusPreferredFile, nonSeam, "the fixture must actually rename the seam signature")
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    algCensusProviderFile,
			algPreferredProviderFile: nonSeam,
		})
		findings := scanAlgTerminalOwnerCensus(files)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGSecondTerminalOwner,
			`declares Decide with a signature that is not the generic terminal-decision seam`),
			"an approved receiver whose Decide is not the seam shape cannot be certified:\n%s", algFixtureRender(findings))
	})

	t.Run("non terminal receivers are not counted", func(t *testing.T) {
		t.Parallel()
		files := parseAlgOverlayFiles(t, map[string]string{
			algLegacyProviderFile:    algCensusProviderFile,
			algPreferredProviderFile: algCensusPreferredFile,
			algFeatureRootDir + "/config.go": `package agentloopguard

type Config struct{ Enabled bool }
`,
			algFeatureRootDir + "/completiontool_handle.go": `package agentloopguard

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"

type completionToolProvider struct{}

func (completionToolProvider) Spec() controltool.Spec { return controltool.Spec{} }
`,
		})
		assert.Empty(t, algFixtureRender(scanAlgTerminalOwnerCensus(files)))
	})
}

// TestTask121FixtureGuardHiddenResurrectionRejected proves the retired
// hidden-content terminal owner cannot come back, in core or anywhere else in
// the production tree.
func TestTask121FixtureGuardHiddenResurrectionRejected(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		relPath string
		src     string
	}{
		{
			name:    "constant on the turn terminal owner",
			relPath: "internal/core/runtime/executor_settlement.go",
			src:     "package runtime\n\ntype turnTerminal struct{}\n\nconst guardHidden turnTerminal = iota\n",
		},
		{
			name:    "field on the turn terminal owner",
			relPath: "internal/core/runtime/terminal_decision_continuation.go",
			src:     "package runtime\n\ntype turnTerminal struct{ guardHidden bool }\n",
		},
		{
			name:    "call site outside core",
			relPath: "internal/infra/runtimebundle/process_services.go",
			src:     "package runtimebundle\n\nfunc settle() { _ = guardHidden }\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := scanAlgOwnershipSyntheticSource(t, tc.relPath, tc.src)
			require.NotEmpty(t, findings, algFixtureRender(findings))
			assert.True(t, algFixtureHasFinding(findings, RuleALGHiddenGuardResurrection,
				`resurrected retired hidden-content terminal owner "guardHidden"`),
				"hidden-guard ratchet must reject this resurrection:\n%s", algFixtureRender(findings))
		})
	}

	t.Run("canonical writer naming passes", func(t *testing.T) {
		t.Parallel()
		src := "package runtime\n\nfunc publish(ctx context.Context, w steeringWriter) error { return w.Write(ctx) }\n"
		assert.Empty(t, scanAlgOwnershipSyntheticSource(t, "internal/core/runtime/terminal_decision_continuation.go", src))
	})
}

// TestTask121FixtureALGRejectsSecondPolicyEndpoint proves design ratchet 6 fires
// when the feature reaches outside its own closure for anything that could own
// a second policy endpoint/store, and that the approved closure passes.
func TestTask121FixtureALGRejectsSecondPolicyEndpoint(t *testing.T) {
	t.Parallel()
	t.Run("a subpackage cannot reach outside the closure either", func(t *testing.T) {
		t.Parallel()
		src := "package verifier\n\nimport _ \"" + algModulePath + "/internal/infra/runtimebundle" + "\"\n"
		findings := scanAlgOwnershipSyntheticSource(t, algFeatureRootDir+"/verifier/verifier.go", src)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGPolicyStoreOwnership, "outside its permitted closure"),
			"a feature subpackage must be held to the same closure as the feature root:\n%s", algFixtureRender(findings))
	})

	t.Run("a feature subpackage append is refused like a root append", func(t *testing.T) {
		t.Parallel()
		src := "package verifier\n\nfunc inject(c lipapi.Call, msg lipapi.Item) {\n\tc.Messages = append(c.Messages, msg)\n}\n"
		findings := scanAlgOwnershipSyntheticSource(t, algFeatureRootDir+"/verifier/verifier.go", src)
		require.NotEmpty(t, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGClientCallAppend, `field "Messages"`),
			"a direct client/A-leg append is forbidden anywhere in the feature:\n%s", algFixtureRender(findings))
	})

	t.Run("positive approved closure passes", func(t *testing.T) {
		t.Parallel()
		// Standard-library imports are outside the feature closure by
		// construction: the toolchain reserves a dotless first path element for
		// them, so a module can never hide behind one.
		src := `package agentloopguard

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

func spec() controltool.Spec { return controltool.Spec{Tool: lipapi.ToolDef{}} }

func decide(ctx context.Context) terminaldecision.Decision {
	return terminaldecision.Decision{ReasonCode: lipapi.Reason(fmt.Sprint("x"))}
}
`
		assert.Empty(t, scanAlgOwnershipSyntheticSource(t, algFeatureRootDir+"/completiontool.go", src))
	})

	for _, tc := range []struct {
		name    string
		imp     string
		wantSub string
	}{
		{
			name:    "constructs a second policy store",
			imp:     algModulePath + "/internal/core/terminaldecisionpolicy",
			wantSub: "outside its permitted closure",
		},
		{
			name:    "reaches an admin endpoint",
			imp:     algModulePath + "/internal/stdhttp/admin",
			wantSub: "outside its permitted closure",
		},
		{
			name:    "reaches a composition root",
			imp:     algModulePath + "/internal/infra/runtimebundle",
			wantSub: "outside its permitted closure",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := "package agentloopguard\n\nimport _ \"" + tc.imp + "\"\n"
			findings := scanAlgOwnershipSyntheticSource(t, algFeatureRootDir+"/policy_store.go", src)
			require.NotEmpty(t, findings, algFixtureRender(findings))
			assert.True(t, algFixtureHasFinding(findings, RuleALGPolicyStoreOwnership, tc.wantSub),
				"policy-store ratchet must reject this dependency:\n%s", algFixtureRender(findings))
		})
	}
}

// algPreferredOverlay is a miniature approved feature package for the preferred
// strategy: the constructor dispatches, the preferred receiver owns only numeric
// limits, and nothing reaches a verifier.
const algPreferredOverlayProvider = `package agentloopguard

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"

type Config struct{ Strategy string }

func NewConfiguredProvider(cfg Config) (terminaldecision.Provider, error) {
	switch cfg.Strategy {
	case StrategySemanticVerifier:
		return provider{}, nil
	case StrategyAttemptCompletion:
		return preferredProvider{maxProtocolReprompts: 1}, nil
	default:
		return nil, nil
	}
}

type provider struct{ verifierRole string }

func (provider) ID() string { return providerID }

func (p provider) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	return allowStop("legacy"), nil
}

func allowStop(reason string) terminaldecision.Decision { return terminaldecision.Decision{ReasonCode: reason} }
`

const algPreferredOverlayReceiver = `package agentloopguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

type preferredProvider struct{ maxProtocolReprompts int }

func (preferredProvider) ID() string { return providerID }

func (p preferredProvider) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	limit := p.limits()
	if limit == 0 {
		return allowStop("no_budget"), nil
	}
	return allowStop("protocol_inactive"), nil
}

func (p preferredProvider) limits() int { return p.maxProtocolReprompts }
`
