package sessionclassification_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type classifierProbe struct{}

func (classifierProbe) ID() string { return "probe" }

func (classifierProbe) Classify(context.Context, sessionclassification.Input) (session.Classification, error) {
	return session.Classification{}, nil
}

var _ sessionclassification.Classifier = classifierProbe{}

func TestToolCategorySetUsesCanonicalToolNameCategories(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want sessionclassification.ToolCategorySet
	}{
		{name: " READ ", want: sessionclassification.ToolCategoryFileRead},
		{name: "grep", want: sessionclassification.ToolCategoryFileSearch},
		{name: "bash", want: sessionclassification.ToolCategoryOSCommand},
		{name: "edit", want: sessionclassification.ToolCategoryFileEdit},
		{name: "delete_file", want: sessionclassification.ToolCategoryFileRemove},
		{name: "web_search", want: sessionclassification.ToolCategoryWebAccess},
		{name: "not-a-canonical-tool", want: sessionclassification.ToolCategoryUnknownSeen},
		{name: "", want: sessionclassification.ToolCategoryUnknownSeen},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := (sessionclassification.ToolCategorySet(0)).AddToolName(tc.name)
			if got != tc.want {
				t.Fatalf("AddToolName(%q) = %016b, want %016b", tc.name, got, tc.want)
			}
		})
	}

	var combined sessionclassification.ToolCategorySet
	for _, name := range []string{"read", "grep", "bash", "edit", "delete_file", "web_search", "unknown_tool", "read"} {
		combined = combined.AddToolName(name)
	}
	want := sessionclassification.ToolCategoryFileRead |
		sessionclassification.ToolCategoryFileSearch |
		sessionclassification.ToolCategoryOSCommand |
		sessionclassification.ToolCategoryFileEdit |
		sessionclassification.ToolCategoryFileRemove |
		sessionclassification.ToolCategoryWebAccess |
		sessionclassification.ToolCategoryUnknownSeen
	if combined != want {
		t.Fatalf("combined tool categories = %016b, want %016b", combined, want)
	}
}

func TestSessionClassificationInputExposesOnlyBoundedMetadataFields(t *testing.T) {
	t.Parallel()

	assertStructFields(t, reflect.TypeOf(sessionclassification.Input{}), map[string]reflect.Type{
		"TraceID":   reflect.TypeOf(""),
		"Session":   reflect.TypeOf(session.SessionView{}),
		"Workspace": reflect.TypeOf(workspace.WorkspaceView{}),
		"Evidence":  reflect.TypeOf(sessionclassification.Evidence{}),
	})
	assertStructFields(t, reflect.TypeOf(sessionclassification.Evidence{}), map[string]reflect.Type{
		"Operation":       reflect.TypeOf(lipapi.Operation("")),
		"ClientUserAgent": reflect.TypeOf(""),
		"ToolCategories":  reflect.TypeOf(sessionclassification.ToolCategorySet(0)),
	})
	if got := reflect.TypeOf(sessionclassification.ToolCategorySet(0)).Kind(); got != reflect.Uint16 {
		t.Fatalf("ToolCategorySet underlying kind = %s, want uint16", got)
	}
}

func TestSessionClassificationPackageHasNoInternalImports(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("go", "list", "-test=false", "-json", ".")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list sessionclassification: %v", err)
	}
	var pkg struct {
		Imports []string `json:"Imports"`
	}
	if err := json.Unmarshal(output, &pkg); err != nil {
		t.Fatalf("decode go list output: %v", err)
	}
	for _, imported := range pkg.Imports {
		if strings.HasPrefix(imported, "internal/") || strings.Contains(imported, "/internal/") {
			t.Errorf("public sessionclassification package imports internal package %q", imported)
		}
	}
}

func assertStructFields(t *testing.T, got reflect.Type, want map[string]reflect.Type) {
	t.Helper()
	if got.Kind() != reflect.Struct {
		t.Fatalf("type %s is %s, want struct", got, got.Kind())
	}
	if got.NumField() != len(want) {
		t.Fatalf("%s has %d fields, want %d", got, got.NumField(), len(want))
	}
	for i := range got.NumField() {
		field := got.Field(i)
		wantType, ok := want[field.Name]
		if !ok {
			t.Errorf("%s has unexpected field %q", got, field.Name)
			continue
		}
		if field.Type != wantType {
			t.Errorf("%s.%s has type %s, want %s", got, field.Name, field.Type, wantType)
		}
	}
}
