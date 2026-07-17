package toolcall

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type Action int

const (
	ActionUnspecified Action = iota
	ActionPass
	ActionRewrite
	ActionReject
)

const (
	ReasonValidPassThrough          = "valid_pass_through"
	ReasonSyntaxRepaired            = "syntax_repaired"
	ReasonToolNameNormalized        = "tool_name_normalized"
	ReasonPropertyRenamed           = "property_renamed"
	ReasonDefaultInserted           = "default_inserted"
	ReasonConstInserted             = "const_inserted"
	ReasonEnumInserted              = "enum_inserted"
	ReasonAdditionalPropertyRemoved = "additional_property_removed"
	ReasonAmbiguousToolName         = "ambiguous_tool_name"
	ReasonAmbiguousProperty         = "ambiguous_property"
	ReasonUnrepairable              = "unrepairable"
	ReasonSchemaInvalid             = "schema_invalid"
	ReasonSchemaUnsupported         = "schema_unsupported"
	ReasonArgsTooLarge              = "args_too_large"
	ReasonScalarCoercionDisabled    = "scalar_coercion_disabled"
	ReasonCanceled                  = "canceled"
)

type CompletedCall struct {
	ToolCallID string
	ToolName   string
	ArgsJSON   []byte
}

type Meta struct {
	TraceID    string
	ALegID     string
	BLegID     string
	AttemptSeq int
}

type Result struct {
	Action     Action
	ToolName   string
	ArgsJSON   []byte
	ReasonCode string
}

type Finalizer interface {
	ID() string
	Order() int
	Finalize(ctx context.Context, call CompletedCall, tool lipapi.ToolDef, catalog []lipapi.ToolDef, meta Meta) (Result, error)
}
