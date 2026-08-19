package archtest

// This file is the Phase-A ownership evidence for the turn/recv simplification.
// It intentionally contains no production hooks: the scanner reads the current
// production AST and compares it with the checked-in baseline artifact. The
// final-topology test is a RED ratchet until the migration has introduced the
// owners described by the spec design.

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const turnRecvOwnershipBaselineRelPath = "internal/archtest/testdata/turn_recv_ownership_baseline.json"

const turnRecvOwnershipSchemaVersion = 1

const runtimeModulePrefix = "github.com/matdev83/go-llm-interactive-proxy/"

var turnRecvOwnershipCategories = []string{
	"immutable_request_fact",
	"current_attempt_state",
	"recovery_routing_state",
	"response_pipeline_state",
	"request_terminal_state",
	"infrastructure_compatibility_state",
}

type turnRecvField struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Category  string   `json:"category"`
	SyncTypes []string `json:"sync_types,omitempty"`
}

type turnRecvSyncPrimitive struct {
	Field string `json:"field"`
	Type  string `json:"type"`
}

type turnRecvMethod struct {
	Name            string `json:"name"`
	File            string `json:"file"`
	Line            int    `json:"line"`
	Responsibility  string `json:"responsibility"`
	ReachesExecutor bool   `json:"reaches_executor"`
}

type turnRecvDomainFanout struct {
	Package string   `json:"package"`
	Methods []string `json:"methods"`
}

type turnRecvStateCopy struct {
	File     string `json:"file"`
	Function string `json:"function"`
	Line     int    `json:"line"`
	Kind     string `json:"kind"`
	Left     string `json:"left"`
	Right    string `json:"right"`
}

type turnRecvMirror struct {
	Fact        string   `json:"fact"`
	Authorities []string `json:"authorities"`
	Evidence    []string `json:"evidence"`
}

type turnRecvExecutorReachability struct {
	FieldPresent        bool     `json:"field_present"`
	MethodCount         int      `json:"method_count"`
	Methods             []string `json:"methods"`
	BroadFieldForbidden bool     `json:"broad_field_forbidden"`
}

type turnRecvCurrentInventory struct {
	FieldCount               int                          `json:"field_count"`
	Fields                   []turnRecvField              `json:"fields"`
	CategoryCounts           map[string]int               `json:"category_counts"`
	SyncPrimitiveCount       int                          `json:"sync_primitive_count"`
	SyncPrimitives           []turnRecvSyncPrimitive      `json:"sync_primitives"`
	MethodCount              int                          `json:"method_count"`
	Methods                  []turnRecvMethod             `json:"methods"`
	ResponsibilityCounts     map[string]int               `json:"responsibility_counts"`
	CrossDomainMethodCount   int                          `json:"cross_domain_method_count"`
	DomainPackageFanoutCount int                          `json:"domain_package_fanout_count"`
	DomainPackageFanout      []turnRecvDomainFanout       `json:"domain_package_fanout"`
	ExecutorReachability     turnRecvExecutorReachability `json:"executor_reachability"`
	StateCopyAssignmentCount int                          `json:"state_copy_assignment_count"`
	StateCopyAssignments     []turnRecvStateCopy          `json:"state_copy_assignments"`
	MirroredFacts            []turnRecvMirror             `json:"mirrored_facts"`
}

type turnRecvTargetTopology struct {
	OwnerNames                        []string `json:"owner_names"`
	FacadeMaxDirectFields             int      `json:"facade_max_direct_fields"`
	FacadeMaxSyncPrimitives           int      `json:"facade_max_sync_primitives"`
	FacadeMaxReceiverMethods          int      `json:"facade_max_receiver_methods"`
	FacadeMaxCrossDomainMethods       int      `json:"facade_max_cross_domain_methods"`
	FacadeMaxDomainPackageFanout      int      `json:"facade_max_domain_package_fanout"`
	FacadeMaxExecutorReachableMethods int      `json:"facade_max_executor_reachable_methods"`
	FacadeMaxStateCopyAssignments     int      `json:"facade_max_state_copy_assignments"`
	ForbiddenDirectFields             []string `json:"forbidden_direct_fields"`
	ExactlyOneAuthorityFacts          []string `json:"exactly_one_authority_facts"`
}

type turnRecvOwnershipBaseline struct {
	SchemaVersion int                      `json:"schema_version"`
	Feature       string                   `json:"feature"`
	Phase         string                   `json:"phase"`
	Source        string                   `json:"source"`
	Current       turnRecvCurrentInventory `json:"current"`
	Target        turnRecvTargetTopology   `json:"target"`
}

type turnRecvASTFile struct {
	RelPath string
	AST     *ast.File
	FSet    *token.FileSet
	Imports map[string]string
}

func TestTurnRecvOwnershipBaselineFileExistsAndSchema(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	baseline, err := loadTurnRecvOwnershipBaseline(root)
	if err != nil {
		t.Fatalf("load turn/recv ownership baseline: %v", err)
	}
	if baseline.SchemaVersion != turnRecvOwnershipSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", baseline.SchemaVersion, turnRecvOwnershipSchemaVersion)
	}
	if baseline.Feature != "turn-recv-terminal-ownership-simplification" {
		t.Fatalf("feature = %q, want turn-recv-terminal-ownership-simplification", baseline.Feature)
	}
	if baseline.Phase != "1.1-current-baseline" {
		t.Fatalf("phase = %q, want 1.1-current-baseline", baseline.Phase)
	}
	if len(baseline.Current.Fields) == 0 || len(baseline.Current.Methods) == 0 {
		t.Fatal("baseline must contain the direct field and receiver-method inventory")
	}
	if len(baseline.Current.MirroredFacts) != 4 {
		t.Fatalf("mirrored_facts = %d, want four commitment/finished/attempt-terminal/request-context facts", len(baseline.Current.MirroredFacts))
	}
	for _, category := range turnRecvOwnershipCategories {
		if _, ok := baseline.Current.CategoryCounts[category]; !ok {
			t.Fatalf("category_counts missing accepted category %q", category)
		}
	}
	if len(baseline.Target.OwnerNames) != 5 {
		t.Fatalf("target owner_names = %d, want five cohesive owners", len(baseline.Target.OwnerNames))
	}
}

func TestTurnRecvOwnershipBaselineMatchesCurrentAST(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	baseline, err := loadTurnRecvOwnershipBaseline(root)
	if err != nil {
		t.Fatalf("load turn/recv ownership baseline: %v", err)
	}
	current, err := scanTurnRecvOwnership(root)
	if err != nil {
		t.Fatalf("scan current retryRecvStream ownership: %v", err)
	}
	if !sameJSON(current, baseline.Current) {
		t.Fatalf("checked-in current baseline does not match deterministic AST scan; run with GENERATE_TURN_RECV_BASELINE=1 to refresh the Phase-A artifact")
	}
}

// TestTurnRecvOwnershipFinalTopology_RED is deliberately failing against the
// flattened pre-migration type. It becomes the green architecture ratchet when
// the final façade is an adapter over facts, attempt, recovery, response, and
// terminal owners.
func TestTurnRecvOwnershipFinalTopology_RED(t *testing.T) {
	if os.Getenv("CHECK_TURN_RECV_FINAL_TOPOLOGY") != "1" {
		t.Skip("set CHECK_TURN_RECV_FINAL_TOPOLOGY=1 to exercise the expected-red final topology ratchet")
	}
	t.Parallel()
	root := repoRoot(t)
	baseline, err := loadTurnRecvOwnershipBaseline(root)
	if err != nil {
		t.Fatalf("load turn/recv ownership baseline: %v", err)
	}
	current, err := scanTurnRecvOwnership(root)
	if err != nil {
		t.Fatalf("scan current retryRecvStream ownership: %v", err)
	}
	findings := evaluateTurnRecvTarget(current, baseline.Target)
	if len(findings) != 0 {
		t.Fatalf("RED ARCHITECTURE DEBT DETECTED: final EventStream topology is not established:\n%s", strings.Join(findings, "\n"))
	}
}

func TestGenerateTurnRecvOwnershipBaseline(t *testing.T) {
	if os.Getenv("GENERATE_TURN_RECV_BASELINE") != "1" {
		t.Skip("set GENERATE_TURN_RECV_BASELINE=1 to regenerate the deterministic Phase-A artifact")
	}
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(turnRecvOwnershipBaselineRelPath))
	baseline, err := loadTurnRecvOwnershipBaseline(root)
	if err != nil {
		t.Fatalf("load target topology before generation: %v", err)
	}
	current, err := scanTurnRecvOwnership(root)
	if err != nil {
		t.Fatalf("scan current retryRecvStream ownership: %v", err)
	}
	baseline.Current = current
	raw, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		t.Fatalf("marshal baseline: %v", err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create baseline directory: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	t.Logf("wrote %s", path)
}

func loadTurnRecvOwnershipBaseline(root string) (turnRecvOwnershipBaseline, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(turnRecvOwnershipBaselineRelPath)))
	if err != nil {
		return turnRecvOwnershipBaseline{}, err
	}
	var baseline turnRecvOwnershipBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		return turnRecvOwnershipBaseline{}, err
	}
	return baseline, nil
}

func sameJSON(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}

func scanTurnRecvOwnership(root string) (turnRecvCurrentInventory, error) {
	files, err := loadTurnRecvASTFiles(root)
	if err != nil {
		return turnRecvCurrentInventory{}, err
	}
	var out turnRecvCurrentInventory
	categoryCounts := make(map[string]int, len(turnRecvOwnershipCategories))
	responsibilityCounts := make(map[string]int)
	var fieldNames = make(map[string]bool)
	var receiverMethods []turnRecvMethod
	var fanout = make(map[string]map[string]bool)
	var syncs []turnRecvSyncPrimitive

	for _, file := range files {
		ast.Inspect(file.AST, func(node ast.Node) bool {
			decl, ok := node.(*ast.GenDecl)
			if !ok || decl.Tok.String() != "type" {
				return true
			}
			for _, spec := range decl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != "retryRecvStream" {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structType.Fields.List {
					typeText := nodeText(field.Type)
					syncTypes := syncTypesFor(typeText)
					for _, name := range field.Names {
						category := turnRecvFieldCategory(name.Name)
						out.Fields = append(out.Fields, turnRecvField{Name: name.Name, Type: typeText, Category: category, SyncTypes: syncTypes})
						fieldNames[name.Name] = true
						categoryCounts[category]++
						for _, syncType := range syncTypes {
							syncs = append(syncs, turnRecvSyncPrimitive{Field: name.Name, Type: syncType})
						}
					}
				}
			}
			return true
		})
	}
	if len(out.Fields) == 0 {
		return turnRecvCurrentInventory{}, fmt.Errorf("retryRecvStream struct not found")
	}

	for _, file := range files {
		for _, decl := range file.AST.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok || funcDecl.Recv == nil || funcDecl.Body == nil || len(funcDecl.Recv.List) == 0 || !isRetryRecvReceiver(funcDecl.Recv.List[0].Type) {
				continue
			}
			name := funcDecl.Name.Name
			reachesExecutor := false
			methodPackages := make(map[string]bool)
			ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
				switch expr := node.(type) {
				case *ast.SelectorExpr:
					if ident, ok := expr.X.(*ast.Ident); ok {
						if ident.Name == turnRecvReceiverName(funcDecl.Recv.List[0].Type) && expr.Sel.Name == "executor" {
							reachesExecutor = true
						}
						if importPath, ok := file.Imports[ident.Name]; ok && strings.HasPrefix(importPath, runtimeModulePrefix) {
							methodPackages[importPath] = true
						}
					}
				}
				return true
			})
			responsibility := turnRecvMethodResponsibility(name)
			method := turnRecvMethod{
				Name: name, File: file.RelPath, Line: file.FSet.Position(funcDecl.Pos()).Line,
				Responsibility: responsibility, ReachesExecutor: reachesExecutor,
			}
			receiverMethods = append(receiverMethods, method)
			responsibilityCounts[responsibility]++
			for importPath := range methodPackages {
				if fanout[importPath] == nil {
					fanout[importPath] = make(map[string]bool)
				}
				fanout[importPath][name] = true
			}
		}
	}

	stateCopies := scanTurnRecvStateCopies(files, fieldNames)
	sort.Slice(out.Fields, func(i, j int) bool { return i < j }) // preserve declaration order explicitly
	sort.Slice(syncs, func(i, j int) bool {
		if syncs[i].Field != syncs[j].Field {
			return syncs[i].Field < syncs[j].Field
		}
		return syncs[i].Type < syncs[j].Type
	})
	sort.Slice(receiverMethods, func(i, j int) bool {
		if receiverMethods[i].File != receiverMethods[j].File {
			return receiverMethods[i].File < receiverMethods[j].File
		}
		if receiverMethods[i].Line != receiverMethods[j].Line {
			return receiverMethods[i].Line < receiverMethods[j].Line
		}
		return receiverMethods[i].Name < receiverMethods[j].Name
	})
	for path, methodSet := range fanout {
		methods := make([]string, 0, len(methodSet))
		for method := range methodSet {
			methods = append(methods, method)
		}
		sort.Strings(methods)
		out.DomainPackageFanout = append(out.DomainPackageFanout, turnRecvDomainFanout{Package: path, Methods: methods})
	}
	sort.Slice(out.DomainPackageFanout, func(i, j int) bool { return out.DomainPackageFanout[i].Package < out.DomainPackageFanout[j].Package })
	out.FieldCount = len(out.Fields)
	out.CategoryCounts = categoryCounts
	out.SyncPrimitives = syncs
	out.SyncPrimitiveCount = len(syncs)
	out.Methods = receiverMethods
	out.MethodCount = len(receiverMethods)
	out.ResponsibilityCounts = responsibilityCounts
	out.CrossDomainMethodCount = 0
	for _, method := range receiverMethods {
		if method.Responsibility != "eventstream_control" && method.Responsibility != "transport_adapter" {
			out.CrossDomainMethodCount++
		}
	}
	out.DomainPackageFanoutCount = len(out.DomainPackageFanout)
	out.ExecutorReachability = turnRecvExecutorReachability{BroadFieldForbidden: true}
	for _, field := range out.Fields {
		if field.Name == "executor" && field.Type == "*Executor" {
			out.ExecutorReachability.FieldPresent = true
		}
	}
	for _, method := range receiverMethods {
		if method.ReachesExecutor {
			out.ExecutorReachability.MethodCount++
			out.ExecutorReachability.Methods = append(out.ExecutorReachability.Methods, method.Name)
		}
	}
	out.StateCopyAssignments = stateCopies
	out.StateCopyAssignmentCount = len(stateCopies)
	out.MirroredFacts = turnRecvMirroredFacts()
	return out, nil
}

func loadTurnRecvASTFiles(root string) ([]turnRecvASTFile, error) {
	dir := filepath.Join(root, "internal", "core", "runtime")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var files []turnRecvASTFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		imports := make(map[string]string)
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return nil, fmt.Errorf("unquote import in %s: %w", entry.Name(), err)
			}
			alias := filepath.Base(importPath)
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			if alias != "_" && alias != "." {
				imports[alias] = importPath
			}
		}
		files = append(files, turnRecvASTFile{RelPath: filepath.ToSlash(filepath.Join("internal", "core", "runtime", entry.Name())), AST: file, FSet: fset, Imports: imports})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })
	return files, nil
}

func nodeText(node ast.Node) string {
	var b strings.Builder
	if err := format.Node(&b, token.NewFileSet(), node); err != nil {
		return fmt.Sprintf("%T", node)
	}
	return b.String()
}

func syncTypesFor(typeText string) []string {
	var types []string
	for _, candidate := range []string{"sync.Mutex", "sync.RWMutex", "sync.Once", "sync.WaitGroup", "atomic.Bool", "atomic.Int32", "atomic.Int64", "atomic.Uint32", "atomic.Uint64", "atomic.Uintptr", "atomic.Pointer"} {
		if strings.Contains(typeText, candidate) {
			types = append(types, candidate)
		}
	}
	return types
}

func isRetryRecvReceiver(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "retryRecvStream"
}

func turnRecvReceiverName(expr ast.Expr) string {
	return "s"
}

func turnRecvFieldCategory(name string) string {
	switch name {
	case "baseline", "aLegID", "traceID", "compactionOpenMeta", "recvViews", "recvViewsOK", "routePrefs", "boundRegistry", "boundRegistryOK", "boundCatalog", "boundCatalogOK", "nativeResolver", "modelViewID", "modelViewIDOK", "secureTurn", "secureTurnOK":
		return "immutable_request_fact"
	case "innerMu", "inner", "bleg", "cand", "authority", "attemptTerm", "accounting", "toolFinal", "promptCacheSource", "promptCacheController":
		return "current_attempt_state"
	case "budget", "ttft", "sel", "requestSize", "session", "excluded", "rng", "lastHardReject", "lastHardTransportReject", "lastAdmissionErr", "isContextLimitExhaustion", "transformExcludes", "affinityKey", "affinitySet", "affinityCommitOnce", "recoverPolicy", "interleaved", "suppressThinker", "suppressVisibleMemo", "lastParallelFailure", "isInterleavedThinker":
		return "recovery_routing_state"
	case "seenEvents", "visibleText", "customer", "secureRecvRecordingHardStop", "gateBuf", "gateDrain", "gateLive", "recoverDrain", "lastAuthorityUsage", "lastCustomerUsage", "toolClass", "eventsMu", "usageMu", "finalStreamObs", "internalUsageKeys", "committedTools":
		return "response_pipeline_state"
	case "committed", "finished", "endOnce", "metering", "requestAuth", "tokenAccountingFinalized", "aScope", "holdALegEnd", "termMu", "requestTerm", "billingLegMu", "billingLegRecorded", "billingCallClosureMu", "billingCallClosureSuccess", "billingAccountID", "billingCustomerPricing", "billingChargePolicy", "billingIdentityStamped", "billingCallID", "billingCallState", "keepwarmArmOnce":
		return "request_terminal_state"
	case "executor", "bus", "cachedCtxMu", "lastParent", "cachedCtx":
		return "infrastructure_compatibility_state"
	default:
		return "infrastructure_compatibility_state"
	}
}

func turnRecvMethodResponsibility(name string) string {
	switch {
	case name == "Recv" || name == "Close":
		return "eventstream_control"
	case name == "loadInner" || name == "storeInner" || name == "takeAndNilInner" || name == "cancelAndCloseInner" || name == "lifecycleAttempt":
		return "current_attempt_state"
	case strings.Contains(name, "Billing") || strings.Contains(name, "Authority") || strings.Contains(name, "TokenAccounting") || strings.Contains(name, "RequestAuthority") || strings.Contains(name, "Cancellation") || strings.Contains(name, "Terminal") || name == "finishALegScope" || name == "commitSuccessfulTurn":
		return "request_terminal_state"
	case strings.Contains(name, "Compaction") || strings.Contains(name, "Traffic") || strings.Contains(name, "ClientFacing") || strings.Contains(name, "Tool") || strings.Contains(name, "Gate") || strings.Contains(name, "Usage") || strings.Contains(name, "Event") || name == "beforeEmitClientFacing":
		return "response_pipeline_state"
	case strings.Contains(name, "Replacement") || strings.Contains(name, "Affinity") || strings.Contains(name, "IdleContext") || strings.Contains(name, "DecisionEvidence") || name == "now":
		return "recovery_routing_state"
	case strings.Contains(name, "Context") || name == "recvHookMeta" || name == "viewsFor" || name == "completionSnapshot" || name == "completionGatesFromContext":
		return "immutable_request_fact"
	default:
		return "infrastructure_compatibility_state"
	}
}

func scanTurnRecvStateCopies(files []turnRecvASTFile, fieldNames map[string]bool) []turnRecvStateCopy {
	var copies []turnRecvStateCopy
	for _, file := range files {
		for _, decl := range file.AST.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok || funcDecl.Body == nil || !turnRecvStateCopyFunction(funcDecl.Name.Name) {
				continue
			}
			ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
				assign, ok := node.(*ast.AssignStmt)
				if !ok || len(assign.Lhs) != len(assign.Rhs) {
					return true
				}
				for i := range assign.Lhs {
					left, leftOK := fieldSelectorText(assign.Lhs[i], fieldNames)
					right, rightOK := fieldSelectorText(assign.Rhs[i], fieldNames)
					if !leftOK && !rightOK {
						continue
					}
					copies = append(copies, turnRecvStateCopy{File: file.RelPath, Function: funcDecl.Name.Name, Line: file.FSet.Position(assign.Pos()).Line, Kind: "assignment", Left: left, Right: right})
				}
				return true
			})
			if funcDecl.Name.Name != "assemble" && funcDecl.Name.Name != "assembleExecutorStream" {
				continue
			}
			ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
				literal, ok := node.(*ast.CompositeLit)
				if !ok || !isRetryRecvType(literal.Type) {
					return true
				}
				for _, element := range literal.Elts {
					keyValue, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := keyValue.Key.(*ast.Ident)
					if !ok || !fieldNames[key.Name] {
						continue
					}
					copies = append(copies, turnRecvStateCopy{File: file.RelPath, Function: funcDecl.Name.Name, Line: file.FSet.Position(keyValue.Pos()).Line, Kind: "assembly_literal", Left: key.Name, Right: nodeText(keyValue.Value)})
				}
				return true
			})
		}
	}
	sort.Slice(copies, func(i, j int) bool {
		if copies[i].File != copies[j].File {
			return copies[i].File < copies[j].File
		}
		if copies[i].Line != copies[j].Line {
			return copies[i].Line < copies[j].Line
		}
		if copies[i].Kind != copies[j].Kind {
			return copies[i].Kind < copies[j].Kind
		}
		return copies[i].Left < copies[j].Left
	})
	return copies
}

func turnRecvStateCopyFunction(name string) bool {
	switch name {
	case "assemble", "assembleExecutorStream", "tryReplacementIteration", "copyBoundModelViews":
		return true
	default:
		return false
	}
}

func fieldSelectorText(expr ast.Expr, fieldNames map[string]bool) (string, bool) {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || !fieldNames[selector.Sel.Name] {
		return "", false
	}
	return nodeText(selector), true
}

func isRetryRecvType(expr ast.Expr) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "retryRecvStream"
}

func turnRecvMirroredFacts() []turnRecvMirror {
	return []turnRecvMirror{
		{Fact: "output_commitment", Authorities: []string{"retryRecvStream.committed", "authorityLifecycle.outputCommitted", "ttftBudget.committed"}, Evidence: []string{"markCommitted stores committed atomic.Bool", "markCommitted calls authority.markOutputCommitted", "markOutputCommitted calls ttft.markCommitted"}},
		{Fact: "finished", Authorities: []string{"retryRecvStream.finished", "streamTerminal request owner state"}, Evidence: []string{"markFinished stores finished atomic.Bool", "runStreamTerminal owns request terminal claim"}},
		{Fact: "attempt_terminal", Authorities: []string{"retryRecvStream.attemptTerm", "authorityLifecycle.terminal", "streamTerminal attempt owner state"}, Evidence: []string{"resetAttemptTerminal replaces attemptTerm during retry", "authority lifecycle has independent settled/released terminal state"}},
		{Fact: "request_context", Authorities: []string{"retryRecvStream.recvViews/secureTurn/routePrefs/model views", "retryRecvStream.cachedCtx/lastParent", "retryRecvStream.metering/requestAuth"}, Evidence: []string{"recvExecContext projects pinned values into cached context", "bare Recv callers rely on stream snapshots", "request-scoped authorities are reattached from stream fields"}},
	}
}

func evaluateTurnRecvTarget(current turnRecvCurrentInventory, target turnRecvTargetTopology) []string {
	var findings []string
	if current.FieldCount > target.FacadeMaxDirectFields {
		findings = append(findings, fmt.Sprintf("direct fields=%d > target max %d", current.FieldCount, target.FacadeMaxDirectFields))
	}
	if current.SyncPrimitiveCount > target.FacadeMaxSyncPrimitives {
		findings = append(findings, fmt.Sprintf("sync primitives=%d > target max %d", current.SyncPrimitiveCount, target.FacadeMaxSyncPrimitives))
	}
	if current.MethodCount > target.FacadeMaxReceiverMethods {
		findings = append(findings, fmt.Sprintf("receiver methods=%d > target max %d", current.MethodCount, target.FacadeMaxReceiverMethods))
	}
	if current.CrossDomainMethodCount > target.FacadeMaxCrossDomainMethods {
		findings = append(findings, fmt.Sprintf("cross-domain receiver methods=%d > target max %d", current.CrossDomainMethodCount, target.FacadeMaxCrossDomainMethods))
	}
	if current.DomainPackageFanoutCount > target.FacadeMaxDomainPackageFanout {
		findings = append(findings, fmt.Sprintf("direct domain package fan-out=%d > target max %d", current.DomainPackageFanoutCount, target.FacadeMaxDomainPackageFanout))
	}
	if current.ExecutorReachability.FieldPresent {
		findings = append(findings, "façade retains broad *Executor field")
	}
	if current.ExecutorReachability.MethodCount > target.FacadeMaxExecutorReachableMethods {
		findings = append(findings, fmt.Sprintf("*Executor-reachable receiver methods=%d > target max %d", current.ExecutorReachability.MethodCount, target.FacadeMaxExecutorReachableMethods))
	}
	if current.StateCopyAssignmentCount > target.FacadeMaxStateCopyAssignments {
		findings = append(findings, fmt.Sprintf("assembly/replacement state-copy assignments=%d > target max %d", current.StateCopyAssignmentCount, target.FacadeMaxStateCopyAssignments))
	}
	for _, forbidden := range target.ForbiddenDirectFields {
		for _, field := range current.Fields {
			if field.Name == forbidden {
				findings = append(findings, "forbidden direct field remains: "+forbidden)
				break
			}
		}
	}
	return findings
}
