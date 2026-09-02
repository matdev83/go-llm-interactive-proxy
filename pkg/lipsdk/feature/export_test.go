package feature

import (
	"maps"
	"reflect"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// GeneratedContributionsForTest is a type alias to the internal generatedContributions type for testing.
type GeneratedContributionsForTest = generatedContributions

// GeneratedFrozenForTest is a type alias to the internal generatedFrozen type for testing.
type GeneratedFrozenForTest = generatedFrozen

var (
	testStorageMu      sync.Mutex
	testContribStorage = make(map[*generatedContributions]map[string]any)
	testFrozenStorage  = make(map[*generatedFrozen]map[string]any)
)

func init() {
	onFreezeGenerated = func(gc *generatedContributions, gf *generatedFrozen) {
		testStorageMu.Lock()
		defer testStorageMu.Unlock()
		if m, ok := testContribStorage[gc]; ok {
			mCopy := make(map[string]any, len(m))
			for k, v := range m {
				mCopy[k] = cloneValue(v)
			}
			testFrozenStorage[gf] = mCopy
		}
	}
	onCloneGenerated = func(src, dst *generatedContributions) {
		testStorageMu.Lock()
		defer testStorageMu.Unlock()
		if m, ok := testContribStorage[src]; ok {
			mCopy := make(map[string]any, len(m))
			for k, v := range m {
				mCopy[k] = cloneValue(v)
			}
			testContribStorage[dst] = mCopy
		}
	}
	onThawGenerated = func(gf *generatedFrozen, gc *generatedContributions) {
		testStorageMu.Lock()
		defer testStorageMu.Unlock()
		if m, ok := testFrozenStorage[gf]; ok {
			mCopy := make(map[string]any, len(m))
			for k, v := range m {
				mCopy[k] = cloneValue(v)
			}
			testContribStorage[gc] = mCopy
		}
	}
	onCloneFrozen = func(src, dst *generatedFrozen) {
		testStorageMu.Lock()
		defer testStorageMu.Unlock()
		if m, ok := testFrozenStorage[src]; ok {
			mCopy := make(map[string]any, len(m))
			for k, v := range m {
				mCopy[k] = cloneValue(v)
			}
			testFrozenStorage[dst] = mCopy
		}
	}
}

// BindGeneratedAccessForTest attaches generated access closures and canonical policy to a Plane[T] for testing.
func BindGeneratedAccessForTest[T any](
	p Plane[T],
	contribute func(*generatedContributions, SourceKind, string, T) error,
	get func(*generatedFrozen) T,
	identity func(*generatedFrozen) (string, bool),
) Plane[T] {
	p.generated = generatedAccess[T]{
		contribute: contribute,
		get:        get,
		identity:   identity,
		policy: &generatedPolicy[T]{
			planeID:                p.ID,
			rules:                  p.Rules,
			nilPolicy:              p.NilPolicy,
			isNil:                  p.IsNil,
			validate:               p.Validate,
			validateIdentity:       p.ValidateIdentity,
			combine:                p.Combine,
			identity:               p.Identity,
			exclusiveConflictError: p.ExclusiveConflictError,
		},
	}
	return p
}

// BindGeneratedTestPlane attaches canonical policy and test eligibility/storage to a Plane[T] for testing.
func BindGeneratedTestPlane[T any](p Plane[T]) Plane[T] {
	return BindGeneratedAccessForTest(
		p,
		func(gc *generatedContributions, source SourceKind, pluginID string, v T) error {
			testStorageMu.Lock()
			defer testStorageMu.Unlock()
			m := testContribStorage[gc]
			if m == nil {
				m = make(map[string]any)
				testContribStorage[gc] = m
			}
			var current T
			if curVal, ok := m[p.ID]; ok {
				if typed, ok := curVal.(T); ok {
					current = typed
				}
			}
			currentCopy := cloneValue(current)
			incoming := cloneValue(v)
			combined, err := p.Combine(source, currentCopy, incoming)
			if err != nil {
				return err
			}
			if anyVal := any(v); anyVal != nil {
				rv := reflect.ValueOf(anyVal)
				if rv.Kind() == reflect.Slice && !rv.IsNil() {
					if anyComb := any(combined); anyComb == nil || isReflectNil(reflect.ValueOf(anyComb)) {
						if typedEmpty, ok := reflect.MakeSlice(rv.Type(), 0, 0).Interface().(T); ok {
							combined = typedEmpty
						}
					}
				}
			}
			m[p.ID] = cloneValue(combined)
			return nil
		},
		func(gf *generatedFrozen) T {
			testStorageMu.Lock()
			defer testStorageMu.Unlock()
			if m, ok := testFrozenStorage[gf]; ok {
				if val, ok := m[p.ID]; ok {
					if typed, ok := val.(T); ok {
						return cloneValue(typed)
					}
				}
			}
			var zero T
			return zero
		},
		func(gf *generatedFrozen) (string, bool) {
			if p.Identity == nil {
				return "", false
			}
			testStorageMu.Lock()
			defer testStorageMu.Unlock()
			if m, ok := testFrozenStorage[gf]; ok {
				if val, ok := m[p.ID]; ok {
					if typed, ok := val.(T); ok {
						return p.Identity(typed)
					}
				}
			}
			return "", false
		},
	)
}

// NewContributionSetWithGeneratedForTest creates a ContributionSet wrapping a generatedContributions pointer for testing.
func NewContributionSetWithGeneratedForTest(gen *generatedContributions) *ContributionSet {
	s := NewContributionSet()
	s.generated = gen
	return s
}

// NewFrozenPlaneSetWithGeneratedForTest creates a FrozenPlaneSet wrapping a generatedFrozen pointer for testing.
func NewFrozenPlaneSetWithGeneratedForTest(frozen *generatedFrozen) FrozenPlaneSet {
	return FrozenPlaneSet{
		frozen: frozen,
	}
}

// NewGeneratedContributionsForTest creates a new generatedContributions for testing.
func NewGeneratedContributionsForTest() *generatedContributions {
	return newGeneratedContributions()
}

// NewMalformedGeneratedFrozenCandidateForTest constructs a test-only generated FrozenPlaneSet
// with specific candidate request transforms and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenCandidateForTest(
	reqTr []request.Transform,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		requestTransforms: cloneSlice(reqTr),
		attemptTransforms: cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenSessionWorkspaceCandidateForTest constructs a test-only generated FrozenPlaneSet
// with candidate session openers, workspace resolvers, and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenSessionWorkspaceCandidateForTest(
	so []session.Opener,
	wr []workspace.Resolver,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		sessionOpeners:     cloneSlice(so),
		workspaceResolvers: cloneSlice(wr),
		attemptTransforms:  cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenSecretGuardsCandidateForTest constructs a test-only generated FrozenPlaneSet
// with candidate secret guards and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenSecretGuardsCandidateForTest(
	sg []secretguard.Guard,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		secretGuards:      cloneSlice(sg),
		attemptTransforms: cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenCompactionCandidateForTest constructs a test-only generated FrozenPlaneSet
// with candidate compaction observers and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenCompactionCandidateForTest(
	obs []compaction.Observer,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		compactionObservers: cloneSlice(obs),
		attemptTransforms:   cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenLocalTurnCandidateForTest constructs a test-only generated FrozenPlaneSet
// with candidate local turn handlers and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenLocalTurnCandidateForTest(
	lt []localturn.Handler,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		localTurnHandlers: cloneSlice(lt),
		attemptTransforms: cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenTerminalDecisionCandidateForTest constructs a test-only generated FrozenPlaneSet
// with candidate terminal decision provider and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenTerminalDecisionCandidateForTest(
	provider terminaldecision.Provider,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		terminalDecisionProvider: provider,
		attemptTransforms:        cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenTerminalDecisionMissingIdentityForTest constructs a test-only generated FrozenPlaneSet
// with candidate terminal decision provider but missing frozen identity.
func NewMalformedGeneratedFrozenTerminalDecisionMissingIdentityForTest(
	provider terminaldecision.Provider,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		terminalDecisionProvider: provider,
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenTerminalDecisionForTest constructs a test-only generated FrozenPlaneSet
// with specific terminal decision provider and cached identity metadata.
func NewMalformedGeneratedFrozenTerminalDecisionForTest(
	provider terminaldecision.Provider,
	id string,
	hasID bool,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		terminalDecisionProvider:      provider,
		terminalDecisionProviderID:    id,
		terminalDecisionProviderHasID: hasID,
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenStreamObserversCandidateForTest constructs a test-only generated FrozenPlaneSet
// with candidate stream observer factories for testing.
func NewMalformedGeneratedFrozenStreamObserversCandidateForTest(
	so []response.StreamObserverFactory,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		streamObserverFactories: cloneSlice(so),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenAttemptTransformsForTest constructs a test-only generated FrozenPlaneSet
// with specific attempt transforms and cached identity metadata.
func NewMalformedGeneratedFrozenAttemptTransformsForTest(
	tr []request.AttemptTransform,
	id string,
	hasID bool,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		attemptTransforms:      cloneSlice(tr),
		attemptTransformsID:    id,
		attemptTransformsHasID: hasID,
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenStreamObserverFactoriesForTest constructs a test-only generated FrozenPlaneSet
// with specific stream observer factories and cached identity metadata.
func NewMalformedGeneratedFrozenStreamObserverFactoriesForTest(
	sof []response.StreamObserverFactory,
	id string,
	hasID bool,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		streamObserverFactories:      cloneSlice(sof),
		streamObserverFactoriesID:    id,
		streamObserverFactoriesHasID: hasID,
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenCompactionPreserversForTest constructs a test-only generated FrozenPlaneSet
// with specific compaction preservers and cached identity metadata.
func NewMalformedGeneratedFrozenCompactionPreserversForTest(
	cp []compaction.Preserver,
	id string,
	hasID bool,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		compactionPreservers:      cloneSlice(cp),
		compactionPreserversID:    id,
		compactionPreserversHasID: hasID,
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewFrozenPlaneSetFromMapForTest creates a map-backed FrozenPlaneSet for testing,
// defensively cloning the input maps to ensure frozen immutability.
func NewFrozenPlaneSetFromMapForTest(values map[string]any, identities map[string]string) FrozenPlaneSet {
	var valuesCopy map[string]any
	var pluginIDsCopy map[string]string
	if values != nil {
		valuesCopy = make(map[string]any, len(values))
		pluginIDsCopy = make(map[string]string, len(values))
		for k, v := range values {
			valuesCopy[k] = cloneAny(v)
			pluginIDsCopy[k] = "test"
		}
	}
	var identitiesCopy map[string]string
	if identities != nil {
		identitiesCopy = make(map[string]string, len(identities))
		maps.Copy(identitiesCopy, identities)
	}
	return FrozenPlaneSet{
		values:     valuesCopy,
		identities: identitiesCopy,
		pluginIDs:  pluginIDsCopy,
		frozen:     nil,
	}
}

// DeclaredHookTargetForTest returns the declared hook target of a PlaneDeclaration if implemented.
func DeclaredHookTargetForTest(decl PlaneDeclaration) HookTarget {
	if htp, ok := decl.(hookTargetProvider); ok {
		return htp.declaredHookTarget()
	}
	return ""
}
