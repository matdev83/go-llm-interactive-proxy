package frontendpipe_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
)

func TestSessionClassificationBaseline_CanonicalAndWireInputs(t *testing.T) {
	t.Parallel()

	const userAgent = "roo-code/3.2.1"
	const routeSelector = "custom:gpt-4o"
	body := []byte(`{"model":"gpt-4o","input":"hello","store":false,"tools":[{"type":"function","name":"read_file","description":"Read a file","parameters":{"type":"object","properties":{}}}]}`)
	headers := make(http.Header)
	headers.Set("User-Agent", userAgent)

	// This bounded-valid identity avoids pinning the current OpenResponses
	// decoder's trim-only acceptance; Task 7.2 should use the shared acceptance
	// helper before asserting canonical/wire evidence parity.
	decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
		RouteSelector: routeSelector,
		Headers:       headers,
	})
	require.NoError(t, err)
	require.Equal(t, userAgent, decoded.Call.Invocation.ClientUserAgent)
	require.Len(t, decoded.Call.Tools, 1)
	require.Equal(t, "read_file", decoded.Call.Tools[0].Name)

	source, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		Memory: body,
		Size:   int64(len(body)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = source.Close() })

	profile := openresponses.NewProfile()
	output, err := profile.CompileProof(context.Background(), frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		Headers:              headers,
		URLPath:              "/openresponses/v1/responses",
		RouteSelector:        routeSelector,
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"custom", "stub"}),
		DefaultRouteSelector: "stub:default",
		RouteFromBodyModel:   true,
		Source:               source,
		BodyBytes:            int64(len(body)),
	})
	require.NoError(t, err)

	proof := output.Proof()
	require.Equal(t, lipapi.OperationOpenResponsesCreate, proof.Operation)
	// The certified wire compiler sees the same bounded request body and its
	// tool catalog, but its current proof retains only the aggregate tool count.
	require.Equal(t, len(decoded.Call.Tools), proof.CompactionFacts.ToolCount)

	// Task 7.1 replaced the pre-carrier shape-absence checks with bounded
	// carrier validation: the proof carries exactly the provider-neutral SDK
	// evidence value and no loose identity or tool-bits field. Task 7.2 owns
	// the accepted User-Agent/tool evidence parity against the canonical call.
	proofType := reflect.TypeFor[largebody.Proof]()
	carrier, ok := proofType.FieldByName("ClassificationEvidence")
	require.True(t, ok, "proof must carry the bounded classification evidence carrier")
	require.Equal(t, reflect.TypeFor[sessionclassification.Evidence](), carrier.Type,
		"the carrier must be the provider-neutral SDK evidence type itself")
	for i := range proofType.NumField() {
		field := proofType.Field(i)
		if field.Type == reflect.TypeFor[sessionclassification.Evidence]() {
			continue
		}
		require.NotEqual(t, "ClientUserAgent", field.Name,
			"no loose client identity field may sit beside the bounded carrier")
		require.NotEqual(t, "ToolCategories", field.Name,
			"no loose tool-bits field may sit beside the bounded carrier")
	}

	stamp, err := largebody.BindAssessmentStamp("gen-session-classification-baseline", proof)
	require.NoError(t, err)
	facts, err := largebody.NewWireTurnFactsFromProof(proof, stamp, "req-baseline", "trace-baseline", "bill-baseline")
	require.NoError(t, err)
	require.NoError(t, facts.AssertNoShadowCall())

	// Task 7.1 must not assert proof-to-wire carrier propagation here: no
	// certified profile compiles the carrier until Task 7.2, so both sides of
	// such a comparison would be the zero value and the assertion would pass
	// vacuously. Task 7.2 owns the accepted User-Agent/tool evidence parity
	// against the canonical call, and the in-boundary propagation proof lives in
	// TestNewWireTurnFactsFromProof_CarriesPopulatedClassificationEvidence.
	require.True(t, proof.ClassificationEvidence.IsZero(),
		"no certified profile compiles classification evidence before Task 7.2")
}
