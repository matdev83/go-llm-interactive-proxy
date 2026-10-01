package archtest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/agentfacts"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	sdkclassification "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
)

func TestSessionClassificationStateAndEvaluationShapesDoNotRetainContent(t *testing.T) {
	t.Parallel()

	// Input is intentionally excluded: the SDK snapshot has a transient
	// WorkspaceView.ProjectRoot field, which policy may inspect but must not
	// retain. These are the current feature-owned configuration/evaluation
	// values and bounded evidence outputs.
	types := []struct {
		name   string
		typeOf reflect.Type
	}{
		{name: "Config", typeOf: reflect.TypeOf(sessionclassification.Config{})},
		{name: "HeuristicConfig", typeOf: reflect.TypeOf(sessionclassification.HeuristicConfig{})},
		{name: "RemoteConfig", typeOf: reflect.TypeOf(sessionclassification.RemoteConfig{})},
		{name: "LocalDecision", typeOf: reflect.TypeOf(sessionclassification.LocalDecision{})},
		{name: "agentfacts.Match", typeOf: reflect.TypeOf(agentfacts.Match{})},
		{name: "sessionclassification.Evidence", typeOf: reflect.TypeOf(sdkclassification.Evidence{})},
		{name: "session.Classification", typeOf: reflect.TypeOf(session.Classification{})},
	}
	for _, tc := range types {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if violations := sessionClassificationRetentionViolations(tc.typeOf, tc.name, make(map[reflect.Type]bool)); len(violations) != 0 {
				t.Fatalf("bounded %s type contains content-retention fields: %s", tc.name, strings.Join(violations, ", "))
			}
		})
	}

	wantDecisionFields := []struct {
		name   string
		typeOf reflect.Type
	}{
		{name: "Promotes", typeOf: reflect.TypeOf(false)},
		{name: "Preserves", typeOf: reflect.TypeOf(false)},
		{name: "Source", typeOf: reflect.TypeOf(session.ClassificationSource(""))},
		{name: "EvidenceCode", typeOf: reflect.TypeOf(session.EvidenceCode(""))},
		{name: "ClientFamily", typeOf: reflect.TypeOf(agentfacts.Family(""))},
	}
	decisionType := reflect.TypeOf(sessionclassification.LocalDecision{})
	if decisionType.NumField() != len(wantDecisionFields) {
		t.Fatalf("LocalDecision has %d fields, want %d bounded scalar fields", decisionType.NumField(), len(wantDecisionFields))
	}
	for i, want := range wantDecisionFields {
		field := decisionType.Field(i)
		if field.Name != want.name || field.Type != want.typeOf {
			t.Fatalf("LocalDecision field %d = %s %s, want %s %s", i, field.Name, field.Type, want.name, want.typeOf)
		}
	}

	categoryType := reflect.TypeOf(sdkclassification.ToolCategorySet(0))
	if categoryType.Kind() != reflect.Uint16 || categoryType.Size() != 2 {
		t.Fatalf("ToolCategorySet = %s (%d bytes), want fixed uint16 bitset", categoryType, categoryType.Size())
	}
}

func TestSessionClassificationRetentionGuardRejectsContentShapedFixture(t *testing.T) {
	t.Parallel()

	type nestedFixture struct {
		Prompt string
	}
	type contentShapedFixture struct {
		Decision      sessionclassification.LocalDecision
		Messages      []string
		Nested        nestedFixture
		ToolArguments map[string]string
		WorkspacePath string
		RawHeaders    map[string][]string
	}

	violations := sessionClassificationRetentionViolations(
		reflect.TypeOf(contentShapedFixture{}), "synthetic", make(map[reflect.Type]bool),
	)
	joined := strings.Join(violations, ", ")
	for _, field := range []string{"Messages", "Prompt", "ToolArguments", "WorkspacePath", "RawHeaders"} {
		if !strings.Contains(joined, "."+field) {
			t.Fatalf("retention guard did not identify synthetic %s field; findings: %s", field, joined)
		}
	}
}

func sessionClassificationRetentionViolations(typ reflect.Type, path string, visited map[reflect.Type]bool) []string {
	if typ.Kind() == reflect.Pointer {
		return sessionClassificationRetentionViolations(typ.Elem(), path, visited)
	}
	if typ.Kind() == reflect.Map {
		return []string{path + " has a dynamic map field"}
	}
	if typ.Kind() == reflect.Interface {
		return []string{path + " has a dynamic interface field"}
	}
	if typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		return sessionClassificationRetentionViolations(typ.Elem(), path+"[]", visited)
	}
	if typ.Kind() != reflect.Struct || visited[typ] {
		return nil
	}
	visited[typ] = true
	defer delete(visited, typ)

	var violations []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fieldPath := path + "." + field.Name
		if sessionClassificationRetentionFieldName(field.Name) {
			violations = append(violations, fieldPath+" has a content-bearing name")
		}
		violations = append(violations, sessionClassificationRetentionViolations(field.Type, fieldPath, visited)...)
	}
	return violations
}

func sessionClassificationRetentionFieldName(name string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(name))
	for _, forbidden := range []string{
		"message", "prompt", "transcript", "argument", "toolarg", "path", "projectroot", "header",
		"requestbody", "responsebody", "rawcontent",
	} {
		if strings.Contains(normalized, forbidden) {
			return true
		}
	}
	return false
}
