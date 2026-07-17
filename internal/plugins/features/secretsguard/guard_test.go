package secretsguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// exactStubMatcher is a test-only Matcher: exact match + redact with '*'.
type exactStubMatcher struct {
	secret  []byte
	refName string
	cat     secretguard.SourceCategory
}

func newExactStub(secret, refName string, cat secretguard.SourceCategory) *exactStubMatcher {
	return &exactStubMatcher{
		secret:  []byte(secret),
		refName: refName,
		cat:     cat,
	}
}

func (m *exactStubMatcher) ScanBytes(_ context.Context, input []byte) ([]secretguard.Finding, error) {
	n := countOccurrences(input, m.secret)
	if n == 0 {
		return nil, nil
	}
	return []secretguard.Finding{{
		SecretRefName:   m.refName,
		SourceCategory:  m.cat,
		OccurrenceCount: n,
	}}, nil
}

func (m *exactStubMatcher) ScanString(ctx context.Context, input string) ([]secretguard.Finding, error) {
	return m.ScanBytes(ctx, []byte(input))
}

func (m *exactStubMatcher) RedactBytes(ctx context.Context, input []byte) ([]byte, []secretguard.Finding, error) {
	findings, err := m.ScanBytes(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	if len(findings) == 0 {
		return append([]byte(nil), input...), nil, nil
	}
	mask := bytes.Repeat([]byte("*"), len(m.secret))
	return bytes.ReplaceAll(input, m.secret, mask), findings, nil
}

func (m *exactStubMatcher) RedactString(ctx context.Context, input string) (string, []secretguard.Finding, error) {
	out, findings, err := m.RedactBytes(ctx, []byte(input))
	return string(out), findings, err
}

type staticResolver struct{ m secretguard.Matcher }

func (r staticResolver) Resolve(context.Context) (secretguard.Matcher, error) { return r.m, nil }

type nilResolver struct{}

func (nilResolver) Resolve(context.Context) (secretguard.Matcher, error) { return nil, nil }

type failingRedactMatcher struct {
	match string
	err   error
}

func (m *failingRedactMatcher) ScanBytes(ctx context.Context, input []byte) ([]secretguard.Finding, error) {
	return m.ScanString(ctx, string(input))
}

func (m *failingRedactMatcher) ScanString(_ context.Context, input string) ([]secretguard.Finding, error) {
	if !strings.Contains(input, m.match) {
		return nil, nil
	}
	return []secretguard.Finding{{
		SecretRefName:   "MATCHED_SECRET",
		SourceCategory:  secretguard.SourceCategoryProxyEnv,
		OccurrenceCount: 1,
	}}, nil
}

func (m *failingRedactMatcher) RedactBytes(ctx context.Context, input []byte) ([]byte, []secretguard.Finding, error) {
	redacted, findings, err := m.RedactString(ctx, string(input))
	return []byte(redacted), findings, err
}

func (m *failingRedactMatcher) RedactString(_ context.Context, input string) (string, []secretguard.Finding, error) {
	if !strings.Contains(input, m.match) {
		return input, nil, nil
	}
	return input, nil, m.err
}

func countOccurrences(haystack, needle []byte) int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return 0
	}
	n := 0
	for i := 0; ; {
		j := bytes.Index(haystack[i:], needle)
		if j < 0 {
			return n
		}
		n++
		i += j + len(needle)
	}
}

func assertNoSyntheticSecrets(t *testing.T, s string) {
	t.Helper()
	for _, v := range testkit.AllSyntheticSecretGuardValues() {
		if v != "" && strings.Contains(s, v) {
			t.Fatalf("secret material leaked into diagnostic string (len=%d)", len(s))
		}
	}
}

func baseCall() lipapi.Call {
	return lipapi.Call{
		ID:      "call-1",
		Session: lipapi.SessionRef{ClientSessionID: "sess-keep"},
		Route:   lipapi.RouteIntent{Selector: "model/keep"},
		Instructions: []lipapi.Message{{
			Role: lipapi.RoleSystem,
			Parts: []lipapi.Part{
				{Kind: lipapi.PartText, Text: "instr clean"},
			},
		}},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				{Kind: lipapi.PartText, Text: "msg clean"},
			},
		}},
		Options: lipapi.GenerationOptions{ReasoningEffort: "keep"},
		Extensions: map[string]json.RawMessage{
			"keep": json.RawMessage(`{"secret_field":"ignored"}`),
		},
	}
}

func cleanStressCall() lipapi.Call {
	return lipapi.Call{
		ID:      "call-clean",
		Session: lipapi.SessionRef{ClientSessionID: "sess-clean"},
		Route:   lipapi.RouteIntent{Selector: "model/clean"},
		Instructions: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("alpha"), lipapi.TextPart("beta")}},
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("gamma")}},
		},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("delta"), lipapi.TextPart("epsilon")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("zeta")}},
		},
		Tools: []lipapi.ToolDef{
			{Name: "tool_alpha", Description: "clean alpha", Parameters: json.RawMessage(`{"a":1,"b":["x","y"]}`)},
			{Name: "tool_beta", Description: "clean beta", Parameters: json.RawMessage(`{"nested":{"ok":true}}`)},
		},
		Options: lipapi.GenerationOptions{ReasoningEffort: "keep"},
		Extensions: map[string]json.RawMessage{
			"a": json.RawMessage(`{"x":1,"y":2}`),
			"b": json.RawMessage(`["one","two","three"]`),
		},
	}
}

func servicesWith(m secretguard.Matcher) secretguard.Services {
	return secretguard.Services{MatcherResolver: staticResolver{m: m}}
}

func TestGuard_nilMatcherPasses(t *testing.T) {
	t.Parallel()
	g := NewGuard(mustCfg(t, ActionBlock))
	call := baseCall()
	call.Messages[0].Parts[0].Text = "has " + testkit.SyntheticOpenAIAPIKey
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, secretguard.Services{
		MatcherResolver: nilResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomePass {
		t.Fatalf("outcome: %q", d.Outcome)
	}
}

func TestGuard_nilResolverPasses(t *testing.T) {
	t.Parallel()
	g := NewGuard(mustCfg(t, ActionBlock))
	call := baseCall()
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, secretguard.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomePass {
		t.Fatalf("outcome: %q", d.Outcome)
	}
}

func TestGuard_block_allLocations_noMutation(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", secretguard.SourceCategoryProxyEnv)
	g := NewGuard(mustCfg(t, ActionBlock))

	call := lipapi.Call{
		Messages: []lipapi.Message{
			{
				Role: lipapi.RoleUser,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartText, Text: "text " + secret},
					{Kind: lipapi.PartJSON, Content: json.RawMessage(`{"k":"` + secret + `","n":1}`)},
					{
						Kind:       lipapi.PartToolResult,
						ToolCallID: "c1",
						Text:       "tr-text " + secret,
						Content:    json.RawMessage(`{"out":"` + secret + `"}`),
					},
					{Kind: lipapi.PartImageRef, ImageRef: "img://" + secret},
				},
			},
		},
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "sys " + secret}},
		}},
		Tools: []lipapi.ToolDef{{
			Name:        "tool_" + secret,
			Description: "desc " + secret,
			Parameters:  json.RawMessage(`{"type":"object","properties":{"p":{"default":"` + secret + `"}}}`),
		}},
		Session: lipapi.SessionRef{ClientSessionID: secret},
		Route:   lipapi.RouteIntent{Selector: secret},
		ID:      secret,
		Extensions: map[string]json.RawMessage{
			"x": json.RawMessage(`"` + secret + `"`),
		},
	}
	before := lipapi.CloneCall(call)

	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		assertNoSyntheticSecrets(t, err.Error())
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomeBlock {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("block must not mutate call")
	}
	wantLocs := map[string]bool{
		"instructions[0].parts[0]": false,
		"messages[0].parts[0]":     false,
		"messages[0].parts[1]":     false,
		"messages[0].parts[2]":     false,
		"tools[0].name":            false,
		"tools[0].description":     false,
		"tools[0].schema":          false,
	}
	for _, f := range d.Findings {
		assertNoSyntheticSecrets(t, f.SecretRefName)
		assertNoSyntheticSecrets(t, f.Location)
		if _, ok := wantLocs[f.Location]; !ok {
			t.Fatalf("unexpected location %q", f.Location)
		}
		wantLocs[f.Location] = true
		if f.SecretRefName != "OPENAI_API_KEY" {
			t.Fatalf("ref: %q", f.SecretRefName)
		}
	}
	for loc, seen := range wantLocs {
		if !seen {
			t.Fatalf("missing finding at %s", loc)
		}
	}
	// Image ref / id / route / session / extensions must not contribute findings.
	for _, f := range d.Findings {
		if strings.Contains(f.Location, "image") || f.Location == "" {
			t.Fatalf("bad location %q", f.Location)
		}
	}
}

func TestGuard_block_toolResultMergesSameLocation(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenRouterAPIKey
	m := newExactStub(secret, "OPENROUTER_API_KEY", secretguard.SourceCategoryProxyEnv)
	g := NewGuard(mustCfg(t, ActionBlock))
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleTool,
			Parts: []lipapi.Part{{
				Kind:       lipapi.PartToolResult,
				ToolCallID: "c1",
				Text:       secret,
				Content:    json.RawMessage(`"` + secret + `"`),
			}},
		}},
	}
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomeBlock {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if len(d.Findings) != 1 {
		t.Fatalf("want 1 merged finding, got %d", len(d.Findings))
	}
	f := d.Findings[0]
	if f.Location != "messages[0].parts[0]" {
		t.Fatalf("location: %q", f.Location)
	}
	if f.OccurrenceCount != 2 {
		t.Fatalf("occurrence_count: got %d want 2", f.OccurrenceCount)
	}
}

func TestGuard_log_deepEqualNoMutation(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticAnthropicSecretGuardKey
	m := newExactStub(secret, "ANTHROPIC_API_KEY", secretguard.SourceCategoryPopularEnv)
	g := NewGuard(mustCfg(t, ActionLog))
	call := baseCall()
	call.Messages[0].Parts[0].Text = "leak " + secret
	before := lipapi.CloneCall(call)

	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomeLog {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("log must leave call deep-equal")
	}
	if d.MutationCount != 0 {
		t.Fatalf("mutation_count: %d", d.MutationCount)
	}
}

func TestGuard_log_passWhenClean(t *testing.T) {
	t.Parallel()
	m := newExactStub(testkit.SyntheticGeminiAPIKey, "GEMINI_API_KEY", secretguard.SourceCategoryPopularEnv)
	g := NewGuard(mustCfg(t, ActionLog))
	call := baseCall()
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomePass {
		t.Fatalf("outcome: %q", d.Outcome)
	}
}

func TestGuard_redact_validatesAndMutates(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", secretguard.SourceCategoryProxyEnv)
	g := NewGuard(mustCfg(t, ActionRedact))
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				{Kind: lipapi.PartText, Text: "hi " + secret},
				{Kind: lipapi.PartJSON, Content: json.RawMessage(fmt.Sprintf(`{"token":%q,"n":42,"ok":true}`, secret))},
			},
		}},
		Tools: []lipapi.ToolDef{{
			Name:        "safe_tool",
			Description: "uses " + secret,
			Parameters:  json.RawMessage(fmt.Sprintf(`{"const":%q,"count":3}`, secret)),
		}},
	}
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		assertNoSyntheticSecrets(t, err.Error())
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomeRedacted {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if d.MutationCount <= 0 {
		t.Fatal("expected mutations")
	}
	if err := call.Validate(); err != nil {
		assertNoSyntheticSecrets(t, err.Error())
		t.Fatalf("redacted call must Validate: %v", err)
	}
	if strings.Contains(call.Messages[0].Parts[0].Text, secret) {
		t.Fatal("text still contains secret")
	}
	if !strings.Contains(call.Messages[0].Parts[0].Text, strings.Repeat("*", len(secret))) {
		t.Fatal("text not redacted with mask")
	}
	var obj map[string]any
	if err := json.Unmarshal(call.Messages[0].Parts[1].Content, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["n"] != float64(42) {
		t.Fatalf("JSON number mutated: %#v", obj["n"])
	}
	if obj["ok"] != true {
		t.Fatalf("JSON bool mutated: %#v", obj["ok"])
	}
	tok, _ := obj["token"].(string)
	if tok == secret || strings.Contains(tok, secret) {
		t.Fatal("JSON string not redacted")
	}
	if !json.Valid(call.Tools[0].Parameters) {
		t.Fatal("tool schema JSON invalid after redact")
	}
	assertNoSyntheticSecrets(t, string(call.Messages[0].Parts[1].Content))
	assertNoSyntheticSecrets(t, call.Tools[0].Description)
}

func TestGuard_redact_noMatchLeavesCallUnchanged(t *testing.T) {
	t.Parallel()
	m := newExactStub(testkit.SyntheticOpenAIAPIKey, "OPENAI_API_KEY", secretguard.SourceCategoryProxyEnv)
	call := cleanStressCall()
	before := lipapi.CloneCall(call)

	redactGuard := NewGuard(mustCfg(t, ActionRedact))
	d, err := redactGuard.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomePass {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("clean redact must leave the call unchanged")
	}
}

func TestGuard_redactMatcherFailureLeavesCallUnchanged(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	call := baseCall()
	call.Messages[0].Parts[0].Text = "match " + secret
	before := lipapi.CloneCall(call)

	cases := []struct {
		name string
		err  error
	}{
		{name: "plain error", err: fmt.Errorf("redact failed")},
		{name: "context canceled", err: context.Canceled},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &failingRedactMatcher{match: secret, err: tc.err}
			g := NewGuard(mustCfg(t, ActionRedact))
			c := call
			_, err := g.Evaluate(t.Context(), &c, secretguard.Meta{}, servicesWith(m))
			if err == nil {
				t.Fatal("expected redaction error")
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("error: %v", err)
			}
			if !reflect.DeepEqual(c, before) {
				t.Fatal("redaction failure must leave the live call unchanged")
			}
		})
	}
}

func TestGuard_redact_invalidJSONOpaque(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticBearerCredential
	m := newExactStub(secret, "request_credential", secretguard.SourceCategoryRequestCred)
	g := NewGuard(mustCfg(t, ActionRedact))
	opaque := []byte("not-json-" + secret)
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartJSON, Content: opaque}},
		}},
	}
	// PartJSON requires valid JSON for Validate — after opaque RedactBytes content may still be invalid.
	// Guard should still redact bytes; Validate is runner-owned. Ensure secret gone from Content.
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomeRedacted {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if bytes.Contains(call.Messages[0].Parts[0].Content, []byte(secret)) {
		t.Fatal("opaque JSON content still contains secret")
	}
}

func TestGuard_scanLimit_blockReturnsBlockWithoutError(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", secretguard.SourceCategoryProxyEnv)
	cfg := mustCfg(t, ActionBlock)
	first := "token=" + secret
	cfg.ScanMaxBytes = len(first) + 1
	g := NewGuard(cfg)
	call := lipapi.Call{
		Messages: []lipapi.Message{
			{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart(first)},
			},
			{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart(strings.Repeat("z", 16))},
			},
		},
	}
	before := lipapi.CloneCall(call)
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if !d.ScanLimitHit {
		t.Fatal("ScanLimitHit want true")
	}
	if d.FailureKind != FailureKindScanLimit {
		t.Fatalf("FailureKind: %q", d.FailureKind)
	}
	if d.Outcome != secretguard.OutcomeBlock {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if len(d.Findings) == 0 {
		t.Fatal("expected findings before scan limit")
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("block must not mutate call")
	}
}

func TestGuard_scanLimit_blockIgnoresBestEffortAudit(t *testing.T) {
	t.Parallel()
	m := newExactStub(testkit.SyntheticOpenAIAPIKey, "OPENAI_API_KEY", secretguard.SourceCategoryProxyEnv)
	cfg := mustCfg(t, ActionBlock)
	cfg.AuditFailurePolicy = AuditBestEffort
	cfg.ScanMaxBytes = 4
	g := NewGuard(cfg)
	call := baseCall()
	call.Messages[0].Parts[0].Text = "12345"
	before := lipapi.CloneCall(call)
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if !d.ScanLimitHit || d.Outcome != secretguard.OutcomeBlock {
		t.Fatalf("decision: %#v", d)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("block scan-limit must not mutate call")
	}
}

func TestGuard_scanLimit_redactReturnsBlockWithoutMutatingLiveCall(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", secretguard.SourceCategoryProxyEnv)
	cfg := mustCfg(t, ActionRedact)
	first := "token=" + secret
	cfg.ScanMaxBytes = len(first) + 1
	g := NewGuard(cfg)
	call := lipapi.Call{
		Messages: []lipapi.Message{
			{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart(first)},
			},
			{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart(strings.Repeat("z", 16))},
			},
		},
	}
	before := lipapi.CloneCall(call)
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if !d.ScanLimitHit || d.FailureKind != FailureKindScanLimit {
		t.Fatalf("decision: %#v", d)
	}
	if d.Outcome != secretguard.OutcomeBlock {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if len(d.Findings) == 0 {
		t.Fatal("expected findings before scan limit")
	}
	if d.MutationCount != 0 {
		t.Fatalf("mutation_count: %d", d.MutationCount)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("redact scan-limit must leave live call unchanged")
	}
	if !strings.Contains(call.Messages[0].Parts[0].Text, secret) {
		t.Fatal("redact scan-limit must not commit partial mutation")
	}
}

func TestGuard_scanLimit_logNoFindings(t *testing.T) {
	t.Parallel()
	m := newExactStub(testkit.SyntheticOpenAIAPIKey, "OPENAI_API_KEY", secretguard.SourceCategoryProxyEnv)
	cfg := mustCfg(t, ActionLog)
	cfg.ScanMaxBytes = 4
	g := NewGuard(cfg)
	call := baseCall()
	call.Messages[0].Parts[0].Text = "12345"
	before := lipapi.CloneCall(call)
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomeLog {
		t.Fatalf("log scan-limit outcome: %q want log", d.Outcome)
	}
	if !d.ScanLimitHit || d.FailureKind != FailureKindScanLimit {
		t.Fatalf("decision: %#v", d)
	}
	if len(d.Findings) != 0 {
		t.Fatalf("want no findings, got %#v", d.Findings)
	}
	if d.MutationCount != 0 {
		t.Fatalf("mutation_count: %d", d.MutationCount)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("log must not mutate on scan limit")
	}
}

func TestGuard_scanLimit_logRetainsFindingsBeforeLimit(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", secretguard.SourceCategoryProxyEnv)
	cfg := mustCfg(t, ActionLog)
	cfg.ScanMaxBytes = len(secret) + 1
	g := NewGuard(cfg)
	call := lipapi.Call{
		Messages: []lipapi.Message{
			{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart(secret)},
			},
			{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("zz")},
			},
		},
	}
	before := lipapi.CloneCall(call)
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomeLog {
		t.Fatalf("log scan-limit outcome: %q want log", d.Outcome)
	}
	if !d.ScanLimitHit || d.FailureKind != FailureKindScanLimit {
		t.Fatalf("decision: %#v", d)
	}
	if d.MutationCount != 0 {
		t.Fatalf("mutation_count: %d", d.MutationCount)
	}
	if len(d.Findings) != 1 {
		t.Fatalf("want 1 finding before limit, got %#v", d.Findings)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("log must not mutate when scan limit truncates")
	}
}

func TestGuard_scanLimit_logContinuesWithSecretPastBound(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", secretguard.SourceCategoryProxyEnv)
	cfg := mustCfg(t, ActionLog)
	cfg.ScanMaxBytes = 8
	g := NewGuard(cfg)
	// Prefix fills the scan budget; secret lives past the bound and must not be detected.
	call := baseCall()
	call.Messages[0].Parts[0].Text = "AAAAAAAA" + secret
	before := lipapi.CloneCall(call)
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomeLog {
		t.Fatalf("log must continue (log): got %q", d.Outcome)
	}
	if !d.ScanLimitHit || d.FailureKind != FailureKindScanLimit {
		t.Fatalf("want scan_limit hit, got %#v", d)
	}
	if len(d.Findings) != 0 {
		t.Fatalf("secret past bound must not be detected under scan limit: %#v", d.Findings)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("log must leave call byte-identical when scan limit truncates")
	}
	for _, needle := range testkit.AllSyntheticSecretGuardNeedles() {
		if strings.Contains(d.FailureReason, needle) {
			t.Fatalf("failure reason must not contain secret needle %q", needle)
		}
	}
}

func TestGuard_dedupeFindings(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticDuplicateValueAliasA
	m := newExactStub(secret, "ALIAS_A", secretguard.SourceCategoryProxyEnv)
	g := NewGuard(mustCfg(t, ActionBlock))
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: secret + " and again " + secret}},
		}},
	}
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Findings) != 1 {
		t.Fatalf("findings: %d", len(d.Findings))
	}
	if d.Findings[0].OccurrenceCount != 2 {
		t.Fatalf("occurrence: %d", d.Findings[0].OccurrenceCount)
	}
}

func TestGuard_failureModeFailClosed(t *testing.T) {
	t.Parallel()
	g := NewGuard(mustCfg(t, ActionLog))
	if g.FailureMode() != secretguard.FailClosed {
		t.Fatalf("FailureMode: %v", g.FailureMode())
	}
	if g.ID() != ID {
		t.Fatalf("ID: %q", g.ID())
	}
}

func TestFeatureBundle(t *testing.T) {
	t.Parallel()
	cfg := mustCfg(t, ActionBlock)
	b := FeatureBundle(cfg)
	if b.SchemaVersion != lipfeature.SchemaVersionV1 {
		t.Fatalf("schema: %d", b.SchemaVersion)
	}
	if len(b.SecretGuards) != 1 {
		t.Fatalf("guards: %d", len(b.SecretGuards))
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestJSONRedact_preservesNumbers(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticGeminiAPIKey
	m := newExactStub(secret, "GEMINI_API_KEY", secretguard.SourceCategoryPopularEnv)
	in := json.RawMessage(fmt.Sprintf(`{"a":%q,"b":7,"c":[1,%q,true],"big":9007199254740993}`, secret, secret))
	out, findings, err := redactJSONPayload(t.Context(), m, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("expected findings")
	}
	if !json.Valid(out) {
		t.Fatal("output not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	root, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("root type: %T", v)
	}
	if root["b"] != json.Number("7") {
		t.Fatalf("number: %#v", root["b"])
	}
	if root["big"] != json.Number("9007199254740993") {
		t.Fatalf("large integer mutated: %#v", root["big"])
	}
	arr, ok := root["c"].([]any)
	if !ok {
		t.Fatalf("array type: %T", root["c"])
	}
	if arr[0] != json.Number("1") || arr[2] != true {
		t.Fatalf("array: %#v", arr)
	}
	tok, _ := root["a"].(string)
	if len(tok) != len(secret) {
		t.Fatalf("decoded string byte length: got %d want %d", len(tok), len(secret))
	}
	assertNoSyntheticSecrets(t, string(out))
}

func TestGuard_redactJSONStringCountsMatchOnce(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", secretguard.SourceCategoryProxyEnv)
	g := NewGuard(mustCfg(t, ActionRedact))
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartJSON, Content: json.RawMessage(`{"token":"` + secret + `"}`)}},
		}},
	}
	d, err := g.Evaluate(t.Context(), &call, secretguard.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomeRedacted {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if len(d.Findings) != 1 {
		t.Fatalf("findings: %#v", d.Findings)
	}
	if d.Findings[0].OccurrenceCount != 1 {
		t.Fatalf("occurrence_count: got %d want 1", d.Findings[0].OccurrenceCount)
	}
	if d.MutationCount != 1 {
		t.Fatalf("mutation_count: %d", d.MutationCount)
	}
	if !json.Valid(call.Messages[0].Parts[0].Content) {
		t.Fatal("redacted JSON must remain valid")
	}
	if bytes.Contains(call.Messages[0].Parts[0].Content, []byte(secret)) {
		t.Fatal("redacted JSON still contains secret")
	}
}

func TestJSONRedact_escapedStringDecodedMatch(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", secretguard.SourceCategoryProxyEnv)
	// Force JSON escapes in the source while keeping decoded UTF-8 equal to secret.
	escaped := strings.Builder{}
	escaped.WriteString(`{"k":"`)
	for _, r := range secret {
		fmt.Fprintf(&escaped, "\\u%04x", r)
	}
	escaped.WriteString(`"}`)
	in := json.RawMessage(escaped.String())
	out, findings, err := redactJSONPayload(t.Context(), m, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings: %d", len(findings))
	}
	if !json.Valid(out) {
		t.Fatal("invalid JSON after redact")
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	tok, _ := root["k"].(string)
	if tok == secret || strings.Contains(tok, secret) {
		t.Fatal("escaped JSON string not redacted after decode")
	}
	if len(tok) != len(secret) {
		t.Fatalf("decoded redacted length=%d want %d", len(tok), len(secret))
	}
	assertNoSyntheticSecrets(t, string(out))
}

func mustCfg(t *testing.T, action string) Config {
	t.Helper()
	cfg, err := DecodeConfig(mustYAML(t, "action: "+action))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
