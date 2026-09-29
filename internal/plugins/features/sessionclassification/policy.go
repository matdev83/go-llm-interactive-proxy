package sessionclassification

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/agentfacts"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type RemoteInput struct {
	Operation          string
	ClientFamily       string
	HasAmbiguousClient bool
	ToolCategories     sdk.ToolCategorySet
	WorkspaceClass     string
	LocalEvidenceCode  string
}

type RemoteDecision struct {
	CodingProbability float64
}

type RemoteDecider interface {
	Decide(context.Context, RemoteInput) (RemoteDecision, error)
}

type Classifier struct {
	cfg    Config
	state  State
	remote RemoteDecider
	now    func() time.Time
}

func NewClassifier(cfg Config, state State, remote RemoteDecider, now func() time.Time) (*Classifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("%s: nil state", ID)
	}
	if cfg.Mode != ModeHeuristic && remote == nil {
		return nil, fmt.Errorf("%s: remote mode requires remote decider", ID)
	}
	if now == nil {
		now = time.Now
	}
	return &Classifier{cfg: cfg, state: state, remote: remote, now: now}, nil
}

func (*Classifier) ID() string { return ID }

func (c *Classifier) Classify(ctx context.Context, in sdk.Input) (session.Classification, error) {
	if c == nil {
		return session.Classification{}, fmt.Errorf("%s: nil classifier", ID)
	}
	if err := in.Evidence.Validate(); err != nil {
		return currentOrUnknown(in.Session), err
	}
	key, err := KeyFromSession(in.Session)
	if err != nil {
		return currentOrUnknown(in.Session), err
	}
	current, found, err := c.state.Load(ctx, key)
	if err != nil {
		return currentOrUnknown(in.Session), err
	}
	if found && current.Classification.IsCodingAgent() {
		return current.Classification, nil
	}
	now := c.now().UTC()
	local, localCode := c.localDecision(in.Evidence, in.Workspace)
	if c.cfg.Mode != ModeJev && local.IsCodingAgent() {
		rec, _, err := c.state.Promote(ctx, key, local, now)
		if err != nil {
			return currentOrUnknown(in.Session), err
		}
		return rec.Classification, nil
	}
	if c.cfg.Mode == ModeHeuristic {
		return currentOrUnknown(in.Session), nil
	}
	claim, _, ok, err := c.state.ClaimRemote(ctx, key, now, c.cfg.Remote.MaxAttemptsPerSession, c.cfg.Remote.LeaseTTL, c.cfg.Remote.RetryBackoff)
	if err != nil || !ok {
		if err != nil {
			return currentOrUnknown(in.Session), err
		}
		return currentOrUnknown(in.Session), nil
	}
	family := ""
	ambiguous := strings.TrimSpace(in.Evidence.ClientUserAgent) != ""
	if m, ok := agentfacts.MatchClientIdentity(in.Evidence.ClientUserAgent); ok {
		family = string(m.Family)
		ambiguous = false
	}
	remoteCtx, cancel := context.WithTimeout(ctx, c.cfg.Remote.Timeout)
	defer cancel()
	decision, decErr := c.remote.Decide(remoteCtx, RemoteInput{
		Operation:          string(in.Evidence.Operation),
		ClientFamily:       family,
		HasAmbiguousClient: ambiguous,
		ToolCategories:     in.Evidence.ToolCategories,
		WorkspaceClass:     workspaceClass(in.Workspace),
		LocalEvidenceCode:  localCode,
	})
	completion := RemoteCompletion{}
	if decErr == nil && decision.CodingProbability >= c.cfg.Remote.PositiveThreshold {
		completion.Proposal = positive("remote_classifier", "remote.jev_positive")
	}
	rec, completeErr := c.state.CompleteRemote(ctx, claim, completion, c.now().UTC())
	if completeErr != nil {
		return currentOrUnknown(in.Session), completeErr
	}
	if decErr != nil {
		return rec.Classification, decErr
	}
	return rec.Classification, nil
}

func (c *Classifier) localDecision(ev sdk.Evidence, ws workspace.WorkspaceView) (session.Classification, string) {
	ua := strings.ToLower(strings.TrimSpace(ev.ClientUserAgent))
	for _, prefix := range c.cfg.Heuristic.IgnoredUserAgentPrefixes {
		if strings.HasPrefix(ua, prefix) {
			return session.Classification{}, "excluded.user_agent_prefix"
		}
	}
	if m, ok := agentfacts.MatchClientIdentity(ev.ClientUserAgent); ok && m.HighConfidence {
		code := "client_family." + string(m.Family)
		return positive("local_identity", code), code
	}
	readSearch := ev.ToolCategories.Has(sdk.ToolFileRead) || ev.ToolCategories.Has(sdk.ToolFileSearch)
	mutation := ev.ToolCategories.Has(sdk.ToolFileEdit) || ev.ToolCategories.Has(sdk.ToolFileRemove)
	command := ev.ToolCategories.Has(sdk.ToolOSCommand)
	if readSearch && mutation && command {
		return positive("local_tooling", "tooling.distinct_coding_cluster"), "tooling.distinct_coding_cluster"
	}
	if readSearch && (mutation || command) && recognizedProjectMarker(ws.Markers) {
		return positive("local_tooling", "tooling.project_marker_cluster"), "tooling.project_marker_cluster"
	}
	return session.Classification{}, ""
}

func positive(source, evidence string) session.Classification {
	return session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.ClassificationSource(source),
		Confidence: session.ConfidenceHigh,
		Evidence:   session.EvidenceCode(evidence),
		Revision:   1,
	}
}

func currentOrUnknown(v session.SessionView) session.Classification {
	if v.Classification.IsCodingAgent() {
		return v.Classification
	}
	return session.Classification{}
}

func recognizedProjectMarker(markers []string) bool {
	for _, marker := range markers {
		base := strings.ToLower(strings.TrimSpace(filepath.Base(marker)))
		switch base {
		case "go.mod", "go.work", "cargo.toml", "pyproject.toml", "package.json", "pom.xml", "build.gradle", "composer.json", "gemfile":
			return true
		}
		if strings.HasSuffix(base, ".sln") || strings.HasSuffix(base, ".csproj") || strings.HasSuffix(base, ".fsproj") || strings.HasSuffix(base, ".vbproj") {
			return true
		}
	}
	return false
}

func workspaceClass(ws workspace.WorkspaceView) string {
	if recognizedProjectMarker(ws.Markers) {
		return "project"
	}
	return "unknown"
}
