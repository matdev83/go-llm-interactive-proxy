package controltool

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	MaxProviderIDBytes  = 128
	MaxReasonCodeBytes  = 96
	MaxInstructionBytes = 4096
	DefaultMaxArgsBytes = 64 * 1024
	MaxArgsBytes        = 64 * 1024
	MaxIdentifierBytes  = 512
	MaxResultBytes      = lipapi.MaxPartTextBytes
)

// ValidateProviderID validates a stable generation identity.
func ValidateProviderID(id string) error {
	if strings.TrimSpace(id) == "" || !utf8.ValidString(id) || len(id) > MaxProviderIDBytes {
		return fmt.Errorf("%w: provider identity must be non-empty valid UTF-8 and at most %d bytes", ErrInvalidProvider, MaxProviderIDBytes)
	}
	return nil
}

// ProviderIdentity safely obtains the stable provider ID.
func ProviderIdentity(provider Provider) (id string, err error) {
	if isNilProvider(provider) {
		return "", ErrInvalidProvider
	}
	defer func() {
		if recover() != nil {
			id = ""
			err = fmt.Errorf("%w: provider identity unavailable", ErrInvalidProvider)
		}
	}()
	id = provider.ID()
	if err := ValidateProviderID(id); err != nil {
		return "", err
	}
	return id, nil
}

// Resolve validates and snapshots the provider's static identity/spec.
func Resolve(provider Provider) (id string, spec Spec, err error) {
	id, err = ProviderIdentity(provider)
	if err != nil {
		return "", Spec{}, err
	}
	defer func() {
		if recover() != nil {
			id = ""
			spec = Spec{}
			err = fmt.Errorf("%w: provider spec unavailable", ErrInvalidProvider)
		}
	}()
	spec = cloneSpec(provider.Spec())
	if err := ValidateSpec(spec); err != nil {
		return "", Spec{}, err
	}
	return id, spec, nil
}

// ValidateProvider validates the generation-time provider contract.
func ValidateProvider(provider Provider) error {
	_, _, err := Resolve(provider)
	return err
}

// ValidateSpec validates bounded model-facing control state.
func ValidateSpec(spec Spec) error {
	if strings.TrimSpace(spec.Tool.Name) == "" || !utf8.ValidString(spec.Tool.Name) || len(spec.Tool.Name) > lipapi.MaxToolNameBytes {
		return fmt.Errorf("%w: invalid tool name", ErrInvalidSpec)
	}
	if strings.TrimSpace(spec.Tool.Name) != spec.Tool.Name || strings.ContainsAny(spec.Tool.Name, " 	
") {
		return fmt.Errorf("%w: tool name must be exact and whitespace-free", ErrInvalidSpec)
	}
	if !utf8.ValidString(spec.Tool.Description) || len(spec.Tool.Description) > lipapi.MaxToolDescriptionBytes {
		return fmt.Errorf("%w: invalid tool description", ErrInvalidSpec)
	}
	if len(spec.Tool.Parameters) == 0 || len(spec.Tool.Parameters) > lipapi.MaxToolParametersBytes || !json.Valid(spec.Tool.Parameters) {
		return fmt.Errorf("%w: invalid tool JSON schema", ErrInvalidSpec)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(spec.Tool.Parameters, &root); err != nil || len(root) == 0 {
		return fmt.Errorf("%w: tool parameters must be a JSON schema object", ErrInvalidSpec)
	}
	if rawType, ok := root["type"]; ok {
		var typ string
		if json.Unmarshal(rawType, &typ) != nil || typ != "object" {
			return fmt.Errorf("%w: tool parameters root type must be object", ErrInvalidSpec)
		}
	}
	if spec.Instruction.Role != lipapi.RoleSystem && spec.Instruction.Role != lipapi.RoleDeveloper {
		return fmt.Errorf("%w: instruction role must be system or developer", ErrInvalidSpec)
	}
	if strings.TrimSpace(spec.Instruction.Text) == "" || !utf8.ValidString(spec.Instruction.Text) || len(spec.Instruction.Text) > MaxInstructionBytes {
		return fmt.Errorf("%w: invalid instruction", ErrInvalidSpec)
	}
	if spec.MaxArgsBytes <= 0 || spec.MaxArgsBytes > MaxArgsBytes {
		return fmt.Errorf("%w: max args must be in [1,%d]", ErrInvalidSpec, MaxArgsBytes)
	}
	return nil
}

// ValidateOutcome validates the private handler result.
func ValidateOutcome(out Outcome) error {
	if !utf8.ValidString(out.ReasonCode) || len(out.ReasonCode) > MaxReasonCodeBytes {
		return fmt.Errorf("%w: invalid reason code", ErrInvalidOutcome)
	}
	switch out.Kind {
	case OutcomeInvalid:
		if out.ResultText != "" {
			return fmt.Errorf("%w: invalid outcome cannot carry result text", ErrInvalidOutcome)
		}
	case OutcomeComplete:
		if strings.TrimSpace(out.ResultText) == "" || !utf8.ValidString(out.ResultText) || len(out.ResultText) > MaxResultBytes {
			return fmt.Errorf("%w: complete outcome requires bounded result text", ErrInvalidOutcome)
		}
	default:
		return fmt.Errorf("%w: unknown outcome kind", ErrInvalidOutcome)
	}
	return nil
}

// ValidateCompletedCall validates generic bounded call metadata before dispatch.
func ValidateCompletedCall(call CompletedCall, spec Spec) error {
	if strings.TrimSpace(call.ToolCallID) == "" || !utf8.ValidString(call.ToolCallID) || len(call.ToolCallID) > MaxIdentifierBytes {
		return fmt.Errorf("%w: invalid tool call id", ErrInvalidOutcome)
	}
	if call.ToolName != spec.Tool.Name {
		return fmt.Errorf("%w: tool name does not match active spec", ErrInvalidOutcome)
	}
	if len(call.ArgsJSON) > spec.MaxArgsBytes {
		return fmt.Errorf("%w: args exceed active limit", ErrInvalidOutcome)
	}
	if !utf8.Valid(call.ArgsJSON) {
		return fmt.Errorf("%w: args are not valid UTF-8", ErrInvalidOutcome)
	}
	return nil
}

func cloneSpec(spec Spec) Spec {
	spec.Tool.Parameters = append([]byte(nil), spec.Tool.Parameters...)
	return spec
}

func isNilProvider(provider Provider) bool {
	if provider == nil {
		return true
	}
	v := reflect.ValueOf(provider)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
