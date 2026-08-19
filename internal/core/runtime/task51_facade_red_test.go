package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestTask51_FinalFacadeHasNoBroadExecutorOrContextCache is the Task 5.1
// characterization gate. It is intentionally RED until the final EventStream
// facade is reduced to owner references and immutable facts.
func TestTask51_FinalFacadeHasNoBroadExecutorOrContextCache(t *testing.T) {
	path := filepath.Join("executor_retry_stream.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var facade *ast.StructType
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "retryRecvStream" {
				continue
			}
			facade, _ = ts.Type.(*ast.StructType)
		}
	}
	if facade == nil {
		t.Fatal("retryRecvStream struct not found")
	}
	got := make(map[string]string)
	for _, field := range facade.Fields.List {
		fieldType := task51ExprName(field.Type)
		if len(field.Names) == 0 {
			t.Errorf("Task 5.1 facade embeds collaborator %q; owner references must be named fields", fieldType)
			continue
		}
		for _, name := range field.Names {
			got[name.Name] = fieldType
		}
	}
	want := map[string]string{
		"facts":            "recvTurnFacts",
		"responsePipeline": "responsePipeline",
		"attempt":          "attemptSlot",
		"terminal":         "turnTerminal",
		"recovery":         "recoveryController",
	}
	for name, fieldType := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("Task 5.1 facade retains non-owner field %q", name)
			continue
		}
		if name != "responsePipeline" && fieldType != want[name] {
			t.Errorf("Task 5.1 facade field %q has type %q, want %q", name, fieldType, want[name])
		}
	}
	for name, fieldType := range want {
		if gotType, ok := got[name]; !ok {
			t.Errorf("Task 5.1 facade is missing owner field %q (%s)", name, fieldType)
		} else if name == "responsePipeline" && gotType != fieldType {
			t.Errorf("Task 5.1 facade embedded field has type %q, want %q", gotType, fieldType)
		}
	}
}

func task51ExprName(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.StarExpr:
		return task51ExprName(expr.X)
	default:
		return ""
	}
}

func TestTask51_FacadeDoesNotLazyInstallResponsePipeline(t *testing.T) {
	for _, name := range []string{"executor_recv_loop.go", "executor_recv_handlers.go", "executor_retry_stream.go"} {
		path := filepath.Join(name)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			fn, ok := node.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || !strings.Contains(task51ExprName(fn.Recv.List[0].Type), "retryRecvStream") {
				return true
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.CallExpr:
					if ident, ok := node.Fun.(*ast.Ident); ok && (ident.Name == "newResponsePipeline" || ident.Name == "newResponsePipelineForExecutor") {
						t.Errorf("Task 5.1 %s lazily constructs responsePipeline via %s", fn.Name.Name, ident.Name)
					}
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "responsePipeline" {
							t.Errorf("Task 5.1 %s lazily assigns responsePipeline", fn.Name.Name)
						}
					}
				}
				return true
			})
			return false
		})
	}
}

// TestTask51_FacadeHasNoOwnerForwardingReceivers keeps direct owner calls at
// their owner boundary. These methods carry no EventStream coordination: they
// only expose response/terminal/attempt owner operations through the facade.
func TestTask51_FacadeHasNoOwnerForwardingReceivers(t *testing.T) {
	forwardingOnly := map[string]string{
		"now":               "responsePipeline.nowTime",
		"isFinished":        "turnTerminal.finished",
		"isCommitted":       "turnTerminal.committed",
		"markCommitted":     "turnTerminal.markCommitted",
		"popToolFinalDrain": "attemptSession.toolFinal.popDrain",
	}
	for _, name := range []string{"executor_retry_stream.go", "executor_recv_loop.go", "executor_recv_handlers.go", "executor_recv_error.go", "executor_settlement.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || !strings.Contains(task51ExprName(fn.Recv.List[0].Type), "retryRecvStream") {
				continue
			}
			if owner, ok := forwardingOnly[fn.Name.Name]; ok {
				t.Errorf("Task 5.1 facade retains owner-forwarding receiver %s; call %s at the owner boundary", fn.Name.Name, owner)
			}
		}
	}
}

func TestTask51RecoveryControllerHasNoBroadExecutorField(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("recovery_controller.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		ts, ok := node.(*ast.TypeSpec)
		if !ok || (ts.Name.Name != "recoveryController" && ts.Name.Name != "recoveryControllerInput") {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, field := range st.Fields.List {
			if task51ExprName(field.Type) == "Executor" {
				t.Errorf("Task 5.1 recovery owner construction retains broad Executor dependency")
			}
		}
		return false
	})
}
