package archtest

import (
	"strings"
	"testing"
)

func TestTask55DeletedBootstrap_DeclFormsRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "func_BuildBootstrap",
			src:  "package runtimebundle\nfunc BuildBootstrap() {}\n",
			want: "func:BuildBootstrap",
		},
		{
			name: "func_AttachReloadHost",
			src:  "package runtimebundle\nfunc AttachReloadHost() {}\n",
			want: "func:AttachReloadHost",
		},
		{
			name: "type_BootstrapResult",
			src:  "package runtimebundle\ntype BootstrapResult struct{}\n",
			want: "type:BootstrapResult",
		},
		{
			name: "type_BootstrapMode",
			src:  "package runtimebundle\ntype BootstrapMode int\n",
			want: "type:BootstrapMode",
		},
		{
			name: "const_BootstrapServe",
			src:  "package runtimebundle\nconst BootstrapServe = 1\n",
			want: "const:BootstrapServe",
		},
		{
			name: "const_BootstrapUnspecified",
			src:  "package runtimebundle\nconst BootstrapUnspecified = 0\n",
			want: "const:BootstrapUnspecified",
		},
		{
			name: "var_BootstrapMode",
			src:  "package runtimebundle\nvar BootstrapMode = 0\n",
			want: "var:BootstrapMode",
		},
		{
			name: "var_BuildBootstrap",
			src:  "package runtimebundle\nvar BuildBootstrap = func() {}\n",
			want: "var:BuildBootstrap",
		},
		{
			name: "const_AttachReloadHost",
			src:  "package runtimebundle\nconst AttachReloadHost = \"gone\"\n",
			want: "const:AttachReloadHost",
		},
		{
			name: "type_BuildBootstrap",
			src:  "package runtimebundle\ntype BuildBootstrap struct{}\n",
			want: "type:BuildBootstrap",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := scanTask55DeletedBootstrapSource("internal/infra/runtimebundle/sneak.go", tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if !findingsContainIdentity(got, tc.want) {
				t.Fatalf("expected %s, got %v", tc.want, got)
			}
		})
	}
}

func TestTask55DeletedBootstrap_CallAndAliasRejected(t *testing.T) {
	t.Parallel()

	direct := `package cmd
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func serve() {
	_, _ = runtimebundle.BuildBootstrap()
	_, _ = runtimebundle.AttachReloadHost()
}
`
	got, err := scanTask55DeletedBootstrapSource("cmd/lipstd/sneak_call.go", direct)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"call:serve->runtimebundle.BuildBootstrap#1",
		"call:serve->runtimebundle.AttachReloadHost#1",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("expected %s, got %v", id, got)
		}
	}

	localAlias := `package cmd
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func serve() {
	boot := runtimebundle.BuildBootstrap
	_, _ = boot()
}
`
	got, err = scanTask55DeletedBootstrapSource("cmd/lipstd/sneak_alias.go", localAlias)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "call:serve->runtimebundle.BuildBootstrap#1") {
		t.Fatalf("local alias call must be detected, got %v", got)
	}

	pkgAlias := `package other
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
var startHost = runtimebundle.AttachReloadHost
func serve() { _, _ = startHost() }
`
	got, err = scanTask55DeletedBootstrapSource("internal/other/sneak_pkg_alias.go", pkgAlias)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "alias:startHost") {
		t.Fatalf("package alias must be detected, got %v", got)
	}
	if !findingsContainIdentity(got, "call:serve->runtimebundle.AttachReloadHost#1") {
		t.Fatalf("package alias call must be detected, got %v", got)
	}

	samePkg := `package runtimebundle
func helper() { BuildBootstrap(); AttachReloadHost() }
`
	got, err = scanTask55DeletedBootstrapSource("internal/infra/runtimebundle/sneak_unqual.go", samePkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"call:helper->runtimebundle.BuildBootstrap#1",
		"call:helper->runtimebundle.AttachReloadHost#1",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("same-package unqualified call missing %s: %v", id, got)
		}
	}
}

func TestTask55DeletedBootstrap_WrapperAndTwoStepRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func BuildBootstrap() {}
func AttachReloadHost() {}
func buildPartial() { BuildBootstrap() }
func attachReload() { AttachReloadHost() }
func serveDual() {
	buildPartial()
	attachReload()
}
`
	got, err := scanTask55DeletedBootstrapSource("internal/infra/runtimebundle/sneak_twostep.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"func:BuildBootstrap",
		"func:AttachReloadHost",
		"wrapper:buildPartial",
		"wrapper:attachReload",
		"twostep:serveDual",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("expected %s, got %v", id, got)
		}
	}
	if !findingsContainIdentity(got, "call:serveDual->buildPartial#1") &&
		!strings.Contains(formatFindings(got), "buildPartial") {
		t.Fatalf("wrapper call from serveDual must be detected, got %v", got)
	}
}

func TestTask55DeletedBootstrap_UnrelatedControlsNotFlagged(t *testing.T) {
	t.Parallel()
	src := `package modelregistry
type Result struct{}
const Serve = 1
func Build() {}
func Attach() {}
`
	got, err := scanTask55DeletedBootstrapSource("internal/core/modelregistry/ok.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unrelated symbols must not be flagged, got %v", got)
	}

	stringOnly := `package runtimebundle
const doc = "BuildBootstrap AttachReloadHost BootstrapResult"
`
	got, err = scanTask55DeletedBootstrapSource("internal/infra/runtimebundle/doc_strings.go", stringOnly)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("string literals must not count as declarations, got %v", got)
	}
}

func TestTask55DeletedBootstrap_UnrelatedPackageSameNamesNotFlagged(t *testing.T) {
	t.Parallel()
	// Negative: unrelated packages may reuse deleted bare names without
	// importing or delegating to runtimebundle deleted callables.
	src := `package other
type BootstrapResult struct{}
type BootstrapMode int
const BootstrapServe = 1
func BuildBootstrap() {}
func BootstrapServeOp() {}
`
	got, err := scanTask55DeletedBootstrapSource("internal/other/unrelated_bootstrap.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unrelated package bare deleted names must not be flagged, got %v", got)
	}
}

func TestTask55DeletedBootstrap_DotImportRejected(t *testing.T) {
	t.Parallel()
	src := `package other
import . "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func serve() {
	_, _ = BuildBootstrap()
}
`
	got, err := scanTask55DeletedBootstrapSource("internal/other/dot_bootstrap.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "dotimport:runtimebundle") {
		t.Fatalf("dot-import of runtimebundle must fail, got %v", got)
	}
	if !findingsContainIdentity(got, "call:serve->runtimebundle.BuildBootstrap#1") {
		t.Fatalf("dot-imported BuildBootstrap call must fail, got %v", got)
	}
}
