package secretaudit_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/secretaudit"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

func TestNewSlogObserver_nilLogger(t *testing.T) {
	t.Parallel()
	if _, err := secretaudit.NewSlogObserver(nil); err == nil {
		t.Fatal("expected error for nil logger")
	}
}

func TestSlogObserver_decisionEvent_noSyntheticSecretValues(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	obs, err := secretaudit.NewSlogObserver(log)
	if err != nil {
		t.Fatal(err)
	}
	ev := secretguard.DecisionEvent{
		Timestamp: time.Unix(10, 0).UTC(),
		EventID:   "evt-1",
		TraceID:   "tr-1",
		Findings: []secretguard.Finding{{
			SecretRefName:   "OPENAI_API_KEY",
			Aliases:         []string{"OPENAI_API_KEY_2"},
			SourceCategory:  secretguard.SourceCategoryProxyEnv,
			Location:        "messages[0].parts[0].text",
			OccurrenceCount: 1,
		}},
		Action:            "block",
		Outcome:           secretguard.OutcomeBlock,
		QuarantineResult:  secretguard.QuarantineResultCommitted,
		BackendDispatched: false,
		GuardID:           "secrets-guard",
	}
	if err := obs.OnSecretDecision(t.Context(), ev); err != nil {
		t.Fatalf("OnSecretDecision: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "lip.secret_guard.decision") {
		t.Fatalf("missing decision message: len=%d", len(out))
	}
	if !strings.Contains(out, "OPENAI_API_KEY") {
		t.Fatal("expected first secret ref name in log")
	}
	if !strings.Contains(out, "OPENAI_API_KEY_2") {
		t.Fatal("expected alias in log")
	}
	if !strings.Contains(out, "messages[0].parts[0].text") {
		t.Fatal("expected finding location in log")
	}
	if !strings.Contains(out, "OccurrenceCount") && !strings.Contains(out, "occurrence_count") {
		t.Fatal("expected finding count in log")
	}
	for _, s := range testkit.AllSyntheticSecretGuardValues() {
		if s != "" && strings.Contains(out, s) {
			t.Fatalf("synthetic secret leaked into slog buffer (len=%d)", len(out))
		}
	}
}

func TestSlogObserver_decisionEvent_uniqueTopLevelFindingsKeys(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	obs, err := secretaudit.NewSlogObserver(log)
	if err != nil {
		t.Fatal(err)
	}
	ev := secretguard.DecisionEvent{
		Timestamp: time.Unix(10, 0).UTC(),
		EventID:   "evt-2",
		Findings: []secretguard.Finding{
			{
				SecretRefName:   "OPENAI_API_KEY",
				Aliases:         []string{"OPENAI_API_KEY_2"},
				SourceCategory:  secretguard.SourceCategoryProxyEnv,
				Location:        "messages[0].parts[0].text",
				OccurrenceCount: 2,
			},
			{
				SecretRefName:   "SLACK_BOT_TOKEN",
				SourceCategory:  secretguard.SourceCategoryPopularEnv,
				Location:        "tools[0].description",
				OccurrenceCount: 1,
			},
		},
		Action:            "redact",
		Outcome:           secretguard.OutcomeRedacted,
		BackendDispatched: false,
		GuardID:           "secrets-guard",
	}
	if err := obs.OnSecretDecision(t.Context(), ev); err != nil {
		t.Fatalf("OnSecretDecision: %v", err)
	}
	out := bytes.TrimSpace(buf.Bytes())
	if !bytes.HasPrefix(out, []byte("{")) {
		t.Fatalf("expected JSON object output, got %q", string(out))
	}
	keys := topLevelJSONKeys(t, out)
	if got := countJSONKey(keys, "findings"); got != 1 {
		t.Fatalf("top-level findings keys: got %d want 1; keys=%v", got, keys)
	}
	if got := countJSONKey(keys, "finding_summary"); got != 1 {
		t.Fatalf("top-level finding_summary keys: got %d want 1; keys=%v", got, keys)
	}
	var decoded struct {
		Findings []struct {
			SecretRefName   string   `json:"secret_ref_name"`
			Aliases         []string `json:"aliases,omitempty"`
			SourceCategory  string   `json:"source_category"`
			Location        string   `json:"location,omitempty"`
			OccurrenceCount int      `json:"occurrence_count"`
		} `json:"findings"`
		FindingSummary struct {
			Count            int      `json:"count"`
			FirstSecretRef   string   `json:"first_secret_ref"`
			SourceCategories []string `json:"source_categories"`
		} `json:"finding_summary"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if len(decoded.Findings) != 2 {
		t.Fatalf("findings length: got %d want 2", len(decoded.Findings))
	}
	if decoded.Findings[0].SecretRefName != "OPENAI_API_KEY" || decoded.Findings[0].Aliases[0] != "OPENAI_API_KEY_2" {
		t.Fatalf("first findings entry: %#v", decoded.Findings[0])
	}
	if decoded.Findings[1].SecretRefName != "SLACK_BOT_TOKEN" {
		t.Fatalf("second findings entry: %#v", decoded.Findings[1])
	}
	if decoded.FindingSummary.Count != 2 {
		t.Fatalf("finding_summary.count: got %d want 2", decoded.FindingSummary.Count)
	}
	if decoded.FindingSummary.FirstSecretRef != "OPENAI_API_KEY" {
		t.Fatalf("finding_summary.first_secret_ref: %q", decoded.FindingSummary.FirstSecretRef)
	}
	if got, want := decoded.FindingSummary.SourceCategories, []string{"popular_env", "proxy_env"}; !equalStrings(got, want) {
		t.Fatalf("finding_summary.source_categories: got %v want %v", got, want)
	}
	for _, s := range testkit.AllSyntheticSecretGuardValues() {
		if s != "" && strings.Contains(string(out), s) {
			t.Fatalf("synthetic secret leaked into slog JSON (len=%d)", len(out))
		}
	}
}

func topLevelJSONKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("read start token: %v", err)
	}
	if tok != json.Delim('{') {
		t.Fatalf("expected JSON object, got %v", tok)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("read key: %v", err)
		}
		key, ok := tok.(string)
		if !ok {
			t.Fatalf("expected string key token, got %T", tok)
		}
		keys = append(keys, key)
		if err := skipJSONValue(dec); err != nil {
			t.Fatalf("skip value for %q: %v", key, err)
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		t.Fatalf("read end token: tok=%v err=%v", tok, err)
	}
	return keys
}

func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			for dec.More() {
				if _, err := dec.Token(); err != nil {
					return err
				}
				if err := skipJSONValue(dec); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := skipJSONValue(dec); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		default:
			return nil
		}
	default:
		return nil
	}
}

func countJSONKey(keys []string, want string) int {
	n := 0
	for _, k := range keys {
		if k == want {
			n++
		}
	}
	return n
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
