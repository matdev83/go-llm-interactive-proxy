package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// Service owns usage-authority orchestration for admission, settlement,
// release, status, and bounded query flows.
type Service struct {
	rules                  RuleSource
	store                  StateStore
	evidence               EvidenceSink
	clock                  Clock
	evaluationTimeout      time.Duration
	cleanupTimeout         time.Duration
	defaultFailureBehavior domain.FailureBehavior
	snapshotMu             sync.RWMutex
	lastSnapshot           RuleSnapshot
	versionSnapshots       map[string]RuleSnapshot
}

// ServiceOptions controls application-level admission behavior without making
// the domain or storage ports aware of runtime configuration.
type ServiceOptions struct {
	EvaluationTimeout      time.Duration
	CleanupTimeout         time.Duration
	DefaultFailureBehavior domain.FailureBehavior
}

const (
	defaultEvaluationTimeout = 250 * time.Millisecond
	defaultCleanupTimeout    = 2 * time.Second
)

// NewService constructs a service with explicit dependencies. A nil clock
// means "use system wall-clock time" (see Service.now).
func NewService(rules RuleSource, store StateStore, evidence EvidenceSink, clock Clock, options ...ServiceOptions) *Service {
	service := &Service{
		rules:                  rules,
		store:                  store,
		evidence:               evidence,
		clock:                  clock,
		evaluationTimeout:      defaultEvaluationTimeout,
		cleanupTimeout:         defaultCleanupTimeout,
		defaultFailureBehavior: domain.FailureBehaviorFailClosed,
	}
	if len(options) > 0 {
		if options[0].EvaluationTimeout > 0 {
			service.evaluationTimeout = options[0].EvaluationTimeout
		}
		if options[0].CleanupTimeout > 0 {
			service.cleanupTimeout = options[0].CleanupTimeout
		}
		if options[0].DefaultFailureBehavior != "" {
			service.defaultFailureBehavior = options[0].DefaultFailureBehavior
		}
	}
	return service
}

func (s *Service) cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := defaultCleanupTimeout
	if s != nil && s.cleanupTimeout > 0 {
		timeout = s.cleanupTimeout
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (s *Service) evaluationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if s == nil || s.evaluationTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.evaluationTimeout)
}

func (s *Service) cacheSnapshot(snap RuleSnapshot) {
	if s == nil {
		return
	}
	s.snapshotMu.Lock()
	s.lastSnapshot = snap
	if ver := strings.TrimSpace(snap.Version); ver != "" {
		if s.versionSnapshots == nil {
			s.versionSnapshots = make(map[string]RuleSnapshot)
		}
		// Deep-copy rules so later publishes cannot mutate cached versions.
		cp := snap
		if len(snap.Rules) > 0 {
			cp.Rules = append([]domain.Rule(nil), snap.Rules...)
		}
		s.versionSnapshots[ver] = cp
	}
	s.snapshotMu.Unlock()
}

func (s *Service) cachedSnapshot() RuleSnapshot {
	if s == nil {
		return RuleSnapshot{}
	}
	s.snapshotMu.RLock()
	snap := s.lastSnapshot
	s.snapshotMu.RUnlock()
	return snap
}

// versionSnapshot returns a previously admitted snapshot by version identity.
func (s *Service) versionSnapshot(version string) (RuleSnapshot, bool) {
	if s == nil {
		return RuleSnapshot{}, false
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return RuleSnapshot{}, false
	}
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	snap, ok := s.versionSnapshots[version]
	return snap, ok
}

// snapshotForSettle resolves rules using the reservation-bound version when set
// (requirements 11.2, 11.4). Missing bound versions fail closed rather than
// silently substituting an unrelated current snapshot.
func (s *Service) snapshotForSettle(ctx context.Context, bound economics.PolicySnapshotRef) RuleSnapshot {
	if ver := strings.TrimSpace(bound.Version); ver != "" {
		if snap, ok := s.versionSnapshot(ver); ok {
			return snap
		}
		current := s.snapshotTolerant(ctx)
		if strings.TrimSpace(current.Version) == ver {
			return current
		}
		return RuleSnapshot{
			ID:      bound.ID,
			Version: ver,
			State:   economics.SnapshotUnavailable,
			Status: domain.AuthorityStatus{
				State:  domain.AuthorityStateUnavailable,
				Reason: domain.StatusReasonBackingUnavailable,
			},
		}
	}
	return s.snapshotTolerant(ctx)
}

func (s *Service) now() time.Time {
	if s != nil && s.clock != nil {
		return s.clock.Now().UTC()
	}
	return time.Now().UTC()
}

// snapshotTolerant fetches the rule snapshot for settlement/release without
// hard-failing when the rule source is unavailable. A nil rule source or a
// snapshot error yields an empty RuleSnapshot so selectedRuleKind returns "" and
// normalization uses Preserve (an empty UnknownAttribution normalizes identically
// to UnknownAttributionPreserve). Settlement/release stay error-tolerant, so
// this never returns an error; fetch the snapshot once and reuse it for both
// normalization and rule-kind derivation.
func (s *Service) snapshotTolerant(ctx context.Context) RuleSnapshot {
	if s == nil || s.rules == nil {
		return RuleSnapshot{}
	}
	snap, err := s.rules.Snapshot(ctx)
	if err != nil {
		return RuleSnapshot{}
	}
	return snap
}

func (s *Service) normalizeAdmissionInput(mode domain.UnknownAttribution, in AdmissionInput, rules []domain.Rule) AdmissionInput {
	in.Scope = mode.NormalizeScope(in.Scope)
	in.Dimensions = mode.NormalizeDimensions(in.Dimensions)
	in.Scope, in.Dimensions = normalizeConfiguredPolicyLabels(mode, in.Scope, in.Dimensions, rules)
	return in
}

func (s *Service) normalizeSettleInput(mode domain.UnknownAttribution, in SettleInput, rules []domain.Rule) SettleInput {
	in.Scope = mode.NormalizeScope(in.Scope)
	in.Scope, _ = normalizeConfiguredPolicyLabels(mode, in.Scope, domain.Dimensions{}, rules)
	for i := range in.Reservations {
		in.Reservations[i].Reservation.Dimensions = mode.NormalizeDimensions(in.Reservations[i].Reservation.Dimensions)
		_, in.Reservations[i].Reservation.Dimensions = normalizeConfiguredPolicyLabels(mode, in.Scope, in.Reservations[i].Reservation.Dimensions, rules)
	}
	return in
}

func (s *Service) normalizeReleaseInput(mode domain.UnknownAttribution, in ReleaseInput, rules []domain.Rule) ReleaseInput {
	in.Scope = mode.NormalizeScope(in.Scope)
	in.Scope, _ = normalizeConfiguredPolicyLabels(mode, in.Scope, domain.Dimensions{}, rules)
	for i := range in.Reservations {
		in.Reservations[i].Reservation.Dimensions = mode.NormalizeDimensions(in.Reservations[i].Reservation.Dimensions)
		_, in.Reservations[i].Reservation.Dimensions = normalizeConfiguredPolicyLabels(mode, in.Scope, in.Reservations[i].Reservation.Dimensions, rules)
	}
	return in
}

// normalizeConfiguredPolicyLabels fills dynamic label dimensions that are
// absent from an incoming scope when the configured unknown-attribution mode
// explicitly maps unknown values to known-empty. A map cannot represent an
// unknown label key, so the configured rule keys provide the only safe set to
// materialize. Preserve and unknown modes leave absent keys absent.
func normalizeConfiguredPolicyLabels(mode domain.UnknownAttribution, view scope.PrincipalScopeView, dims domain.Dimensions, rules []domain.Rule) (scope.PrincipalScopeView, domain.Dimensions) {
	if mode != domain.UnknownAttributionKnownEmpty {
		return view, dims
	}
	keys := make(map[string]struct{})
	for _, rule := range rules {
		for key := range rule.Match.Labels {
			if domain.IsSafeLabelKey(key) {
				keys[key] = struct{}{}
			}
		}
	}
	if len(keys) == 0 {
		return view, dims
	}
	if view.PolicyLabels == nil {
		view.PolicyLabels = make(map[string]string, len(keys))
	}
	if dims.PolicyLabels == nil {
		dims.PolicyLabels = make(map[string]scope.Value, len(keys))
	}
	for key := range keys {
		if _, ok := view.PolicyLabels[key]; !ok {
			view.PolicyLabels[key] = ""
		}
		if _, ok := dims.PolicyLabels[key]; !ok {
			dims.PolicyLabels[key] = scope.Known("")
		}
	}
	return view, dims
}

func (s *Service) snapshot(ctx context.Context) (RuleSnapshot, error) {
	if s == nil || s.rules == nil {
		return RuleSnapshot{}, WrapError(ErrUnavailable, "snapshot", errors.New("rule source not configured"))
	}
	snap, err := s.rules.Snapshot(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return RuleSnapshot{}, WrapError(ErrEvaluationTimeout, "snapshot", err)
		}
		return RuleSnapshot{}, WrapError(ErrUnavailable, "snapshot", err)
	}
	if snap.FetchedAt.IsZero() {
		snap.FetchedAt = s.now()
	}
	s.cacheSnapshot(snap)
	return snap, nil
}

func (s *Service) readiness(ctx context.Context, fallback domain.AuthorityStatus) (domain.AuthorityStatus, error) {
	if s != nil && s.store != nil {
		status, err := s.store.CheckReadiness(ctx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return domain.AuthorityStatus{}, WrapError(ErrEvaluationTimeout, "readiness", err)
			}
			return domain.AuthorityStatus{}, WrapError(ErrUnavailable, "readiness", err)
		}
		if status.State == "" {
			if fallback.State != "" {
				return fallback, nil
			}
			return domain.AuthorityStatus{
				State:  domain.AuthorityStateUnavailable,
				Reason: domain.StatusReasonBackingUnavailable,
			}, nil
		}
		return status, nil
	}
	return fallback, nil
}

// readinessForEvidence fetches the live readiness status for settlement/release
// evidence without hard-failing when the backing store is unavailable. On error
// it falls back to the snapshot's status (or an empty AuthorityStatus when the
// snapshot carried none). Settlement/release stay error-tolerant, so this never
// returns an error.
func (s *Service) readinessForEvidence(ctx context.Context, fallback domain.AuthorityStatus) domain.AuthorityStatus {
	status, err := s.readiness(ctx, fallback)
	if err != nil {
		return fallback
	}
	return status
}

func (s *Service) admissionStatus(ctx context.Context, snap RuleSnapshot) (domain.AuthorityStatus, error) {
	status, err := s.readiness(ctx, snap.Status)
	if err != nil {
		return domain.AuthorityStatus{}, err
	}
	return status, nil
}

func (s *Service) projectAdmissionEvidence(ctx context.Context, in AdmissionInput, res AdmissionResult, status domain.AuthorityStatus, rules []domain.Rule, now time.Time, reasonOverride policydecision.AccountingReasonCode) (policyAndControlPlane, error) {
	selectedRuleID := res.SelectedRuleID
	if selectedRuleID == "" {
		selectedRuleID = firstRuleID(res.RuleIDs, "")
	}
	ruleKind := res.RuleKind
	if ruleKind == "" {
		ruleKind = selectedRuleKind([]string{selectedRuleID}, rules)
	}
	evidenceAmount := admissionEvidenceAmount(in, res, selectedRuleID, rules)
	var requestedMax, effectiveMax domain.Amount
	clampReason := ""
	if res.Clamp != nil {
		requestedMax = res.Clamp.RequestedMax
		effectiveMax = res.Clamp.EffectiveMax
		clampReason = res.Clamp.Reason
	}
	reserved := res.Reserved && res.ReservationID != ""
	reason := reasonForAdmission(res.Outcome, status, ruleKind, reserved, res.ReservationID)
	if reasonOverride != "" {
		reason = reasonOverride
	}
	evidence, err := projectAuthorityEvidence(status, reserved, Evidence{
		At:               now,
		Correlation:      in.Correlation,
		Scope:            in.Scope,
		RuleID:           selectedRuleID,
		MatchedRuleIDs:   append([]string(nil), res.RuleIDs...),
		RuleType:         string(ruleKind),
		RequestedMax:     requestedMax,
		EffectiveMax:     effectiveMax,
		ClampReason:      clampReason,
		Outcome:          sdkOutcomeFromAdmission(res.Outcome),
		ReasonCode:       reason,
		ReservationID:    res.ReservationID,
		SettlementState:  settlementStateForAdmission(res.Reserved),
		Unit:             string(evidenceAmount.Unit),
		Currency:         evidenceAmount.Currency,
		Reserved:         res.ReservedAmount.Value,
		Authority:        in.Authority,
		Stage:            feature.StageIDPreRequest,
		BackendAttempted: false,
		OutputCommitted:  false,
	})
	if err != nil {
		return policyAndControlPlane{}, err
	}
	if s != nil && s.evidence != nil {
		if err := s.evidence.RecordPolicyDecision(ctx, evidence.Policy); err != nil {
			return policyAndControlPlane{}, WrapError(ErrUnavailable, "admission evidence", err)
		}
		if err := s.evidence.RecordAccountingAuthority(ctx, evidence.Event); err != nil {
			return policyAndControlPlane{}, WrapError(ErrUnavailable, "admission evidence", err)
		}
	}
	return evidence, nil
}

// admissionEvidenceAmount derives the unit/currency basis for the decisive
// rule rather than borrowing the first matched rule's request basis. This is
// important when one request matches quota, rate, and budget rules together.
func admissionEvidenceAmount(in AdmissionInput, res AdmissionResult, selectedRuleID string, rules []domain.Rule) domain.Amount {
	for _, reservation := range res.Reservations {
		if reservation.RuleID == selectedRuleID && reservation.ReservedAmount.Unit != "" {
			return reservation.ReservedAmount
		}
	}
	if res.ReservedAmount.Unit != "" && (selectedRuleID == "" || len(res.Reservations) <= 1) {
		return res.ReservedAmount
	}
	if res.Clamp != nil && res.Clamp.RuleID == selectedRuleID && res.Clamp.EffectiveMax.Unit != "" {
		return res.Clamp.EffectiveMax
	}
	rule, ok := ruleByID(rules, selectedRuleID)
	if !ok {
		return in.Request
	}
	unit := rule.Unit
	if unit == "" {
		unit = rule.Limit.Unit
	}
	amount := in.Request
	switch {
	case unit == domain.AmountUnitMoneyNano:
		amount = in.Spend
	case unit == domain.AmountUnitRequests && in.RequestCount.Unit != "":
		amount = in.RequestCount
	case unit != "":
		if usage, found := in.PreflightUsage.AmountForUnit(unit); found {
			amount = usage
		}
	}
	if amount.Unit == "" {
		amount.Unit = unit
	}
	if amount.Currency == "" {
		amount.Currency = rule.Currency
	}
	return amount
}

type policyAndControlPlane struct {
	Policy           policydecision.Record
	Event            controlplane.Event
	PolicyRecords    []policydecision.Record
	AccountingEvents []controlplane.Event
}
