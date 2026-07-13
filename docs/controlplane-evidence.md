# Control-plane evidence: four-layer guard regression pattern

Every control-plane evidence record passes through four checkpoints before it can land in the durable store or surface to an operator. The audit property — *a guard added at one layer must remain effective at all four layers even after a future refactor* — is regression-locked by a coordinated test at each layer. This document describes the four layers, what each layer owns, and the exact rules for adding a new guard so the property holds.

## Why four layers

The control-plane evidence pipeline is a defense-in-depth chain:

1. **SDK guard** — `pkg/lipsdk/controlplane.Event.Validate()` in `pkg/lipsdk/controlplane/types.go`. Enforces public type-level invariants that any tooling building an `Event` must satisfy.
2. **Core validator** — `internal/core/controlplane.ValidateEvent` in `internal/core/controlplane/validate.go`. Reuses the SDK guard and adds core-owned invariants not safe to put in the SDK (source-name presence, summary safety vs. token-like content, bounded summary size, bounded scope map).
3. **Normalizer** — `internal/core/controlplane.Normalizer` in `internal/core/controlplane/normalizer.go`. Bridges source records (`auth.AuthDecisionEvent`, `usage.Event`, `policydecision.Record`, etc.) into `cp.Event`. Every `From*` method produces exactly one detail block, projects scope from known safe fields, and fills in `OccurredAt` from the source record and `RecordedAt` from the supplied clock.
4. **Recorder** — `internal/core/controlplane.RecorderService` in `internal/core/controlplane/recorder.go`. Reuses the core validator in `prepareAppend` and wraps any validation error as `ErrUnsafeEvidence` (programmer error, never transient). The store is only touched when the event is valid; capability status is only degraded for transient store failures.

Each layer has a single, well-defined job. A future refactor that weakens one layer — for example, demoting `ErrUnsafeEvidence` to a silent no-op, or skipping `ValidateEvent` in `prepareAppend` — is caught by the test at the relevant layer, provided a test exists at that layer. Without the four-layer test, a refactor could silently change semantics and ship.

## What belongs at each layer

| Layer | Owns | Does NOT own |
| --- | --- | --- |
| SDK (`pkg/lipsdk/controlplane`) | `Category`, `Visibility`, `EvidenceState`, `RedactionState` enum membership; zero/missing/`Before` checks for `OccurredAt` / `RecordedAt`; exactly-one-detail invariant; detail-vs-category matchup; `privilegedVisibility` requires `privilegedRedaction`. | Anything that depends on proxy source identification, prompts, provider payloads, or rate-limit semantics. Source-name presence/length cap and bounded summary/scope (those belong to core, not the SDK). |
| Core validator (`internal/core/controlplane/validate.go`) | Source-name presence + length cap (`MaxSourceNameLen`); summary size cap (`MaxSummaryBytes`) + token-like-content rejection (`Bearer`, `api key`, `authorization:`, `oauth`, etc.); bounded scope maps (`MaxScopeMapEntries`, `MaxScopeMapValue`). | Anything that requires a default value the core could change. The validator's only input is the `cp.Event` itself. |
| Normalizer (`internal/core/controlplane/normalizer.go`) | Exactly-one detail block per output; scope projected from optional safe scope view (nil → unknown); timestamps normalized (`OccurredAt` from source, `RecordedAt` from constructor's `Clock`); source identification always set from constructor's `SourceRef`; rejection of free-text fields carrying token-like content. | Inlining raw credentials, raw user input, or provider payloads into `Summary` or `Scope`; producing a detail-vs-category mismatch. |
| Recorder (`internal/core/controlplane/recorder.go`) | Disabled policy → `ErrDisabled`; valid event → store append with no status change; transient store failure → status degraded with `cp.ReasonRecordingFailure` (best-effort) or fail-closed (required pre-work); any validation failure → `ErrUnsafeEvidence` wrap, no store call, **no status degradation**. | Silently dropping unsafe evidence; degrading capability status for a programmer error; retrying or replacing after a `prepareAppend` failure (validation is not transient). |

Category-detail, unknown-enum, and ordering guards still need four-layer coverage when introduced — the table above maps *"what belongs here"* at a high level but is not exhaustive for guard-by-guard auditing.

## Substring assertion rules

Each test at every layer asserts the **explicit-guard substring** — the unique phrase from the guard's `fmt.Errorf` format string — so the test cannot pass for the wrong reason:

- **Uniqueness.** The substring must appear in exactly one guard's error. If two guards can produce the same substring, a refactor that swaps one for the other will silently satisfy the test.
- **Match the guard's wording.** Always assert on the phrase written by the guard. Do not introduce aliases. If the guard says `"scope.principal_id is required"`, do not assert `"principal id"` — the test must fail if someone weakens or replaces the guard wording.
- **Numeric values.** If the format string includes a numeric value (e.g., `"%d bytes"`, `"%d entries"`), assert on the **static prefix**, not the full message. A constant change should not break the test. Example: assert `"summary exceeds"` rather than `"summary exceeds 4096 bytes"`.
- **Exact path.** For scoped errors, include the scope leaf (e.g., `"scope.policy_labels exceeds"` rather than `"scope exceeds"`). The leaf tells you which bound fired.
- **Wrap-passthrough.** At the recorder layer the substring must be matched against the wrapped error (`ErrUnsafeEvidence` is a `%w` wrap, not a substring replacement). `strings.Contains(err.Error(), …)` works because `%w` appends the wrapped message.

## Mutation rules

The mutation that triggers the guard must be the **minimum change** that triggers it and nothing else:

- **Do not break other invariants.** If the mutation also leaves a different field invalid, the test is not exercising the new guard — a future fix to the new guard could be hidden by an earlier failure.
- **Use the normalizer chain** at the normalizer and recorder layers (`n.FromAuthDecision(...)`) so the test exercises the end-to-end path, not just the validator in isolation.
- **For timestamp ordering**, use two non-zero timestamps with `RecordedAt` earlier than `OccurredAt`. The zero checks fire first, so naive `RecordedAt = time.Time{}` mutations cannot independently exercise the ordering guard.
- **For size guards**, mutate by exactly one unit past the cap (e.g., `MaxSummaryBytes+1`). Mutating by a million bytes hides boundary-specific bugs.
- **For visibility/redaction pairings**, mutate exactly one of the two fields. Mutating both at once masks whether each guard fires correctly.

## Code templates

When you add a new control-plane guard, lock it in this order — SDK, core, normalizer, recorder — so the audit property holds. The substitute `MyNewGuard` is used to mark each insertion point.

### 1. SDK layer — `pkg/lipsdk/controlplane/types_test.go`

SDK tests typically set up the validated base event inline (there is no shared helper named `baseValidEvent`). The pattern below mirrors the existing tests in `types_test.go` such as `TestEventValidateRejectsZeroRecordedAt` and `TestEventValidateRejectsPrivilegedVisibilityWithoutPrivilegedRedaction`.

```go
func TestEventValidateRejectsMyNewGuard(t *testing.T) {
    t.Parallel()
    now := time.Now()
    ev := cp.Event{
        Category:       cp.CategoryAuth,
        OccurredAt:     now,
        RecordedAt:     now,
        Visibility:     cp.VisibilityDefault,
        EvidenceState:  cp.EvidenceRecorded,
        RedactionState: cp.RedactionNone,
        Auth:           &cp.AuthDetail{Outcome: "allow"},
        Source:         cp.SourceRef{Name: "test"},
    }
    ev.SomethingRelevant = invalidValue
    err := ev.Validate()
    if err == nil {
        t.Fatalf("SDK guard must reject")
    }
    if !strings.Contains(err.Error(), "my explicit-guard substring") {
        t.Fatalf("error must mention explicit-guard 'my explicit-guard substring', got: %v", err)
    }
}
```

### 2. Core validator — `internal/core/controlplane/validate_test.go`

```go
func TestValidateRejectsMyNewGuard(t *testing.T) {
    t.Parallel()
    ev := validBaseEvent()
    // Make sure every earlier guard (single detail, source name, summary
    // safety, etc.) still passes on this base; only the new mutation can fire.
    ev.SomethingRelevant = invalidValue
    err := controlplane.ValidateEvent(ev)
    if err == nil {
        t.Fatalf("core validator guard must reject")
    }
    if !strings.Contains(err.Error(), "my explicit-guard substring") {
        t.Fatalf("error must mention explicit-guard 'my explicit-guard substring', got: %v", err)
    }
}
```

### 3. Normalizer layer — `internal/core/controlplane/normalizer_test.go`

```go
func TestNormalizerEventWithMyNewGuardRejected(t *testing.T) {
    t.Parallel()
    n := newTestNormalizer(t)
    ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
        Time:    time.Now(),
        TraceID: "trace-1",
        Outcome: auth.OutcomeAllow,
        Scope:   new(knownScopeView()),
    })
    if err != nil {
        t.Fatalf("FromAuthDecision: %v", err)
    }
    // Mutate the output of the normalizer so the SDK and core
    // validator guard chain can fire:
    ev.SomethingRelevant = invalidValue
    err = controlplane.ValidateEvent(ev)
    if err == nil {
        t.Fatalf("normalizer → validator chain must reject")
    }
    if !strings.Contains(err.Error(), "my explicit-guard substring") {
        t.Fatalf("error must mention explicit-guard 'my explicit-guard substring', got: %v", err)
    }
}
```

### 4. Recorder layer — `internal/core/controlplane/recorder_test.go`

```go
func TestRecorderRejectsMyNewGuardAsUnsafeEvidence(t *testing.T) {
    t.Parallel()
    n := controlplane.NewNormalizer(
        fixedClock{t: time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)},
        cp.SourceRef{Name: "test-source", Version: "v1"},
        controlplane.NewScopeFlattener(),
    )
    ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
        Time:    time.Date(2026, 7, 4, 0, 1, 0, 0, time.UTC),
        TraceID: "trace-1",
        Outcome: auth.OutcomeAllow,
    })
    if err != nil {
        t.Fatalf("FromAuthDecision: %v", err)
    }
    ev.SomethingRelevant = invalidValue

    store := &recordingStore{}
    status := controlplane.NewStatus(cp.CapabilityStatus{
        State:           cp.CapabilityReady,
        RecordingPolicy: cp.RecordingBestEffort,
    })
    rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
        Policy: cp.RecordingBestEffort,
        Clock:  fixedClock{t: time.Now()},
    })

    _, err = rec.Record(context.Background(), ev)
    if !errors.Is(err, controlplane.ErrUnsafeEvidence) {
        t.Fatalf("recorder must classify as ErrUnsafeEvidence (programmer error), got %v", err)
    }
    if !strings.Contains(err.Error(), "my explicit-guard substring") {
        t.Fatalf("error must surface explicit-guard 'my explicit-guard substring', got: %v", err)
    }
    if store.appends.Load() != 0 {
        t.Fatalf("recorder must not call store.Append for unsafe evidence, got %d appends",
            store.appends.Load())
    }
    if got := status.Snapshot().State; got != cp.CapabilityReady {
        t.Fatalf("recorder must not degrade status for programmer error, got %q", got)
    }
}
```

Every test uses `t.Parallel()` so the audit suite stays cheap. The recorder template includes the four full assertions (classification, substring, store-not-called, status-not-degraded) because each is independently load-bearing — a refactor that, say, logs the error to the store but does not wrap as `ErrUnsafeEvidence` will still fail on the substring assertion.

## Why `ErrUnsafeEvidence` does not degrade status

`ErrUnsafeEvidence` is wrapped by the recorder's `prepareAppend` whenever `ValidateEvent(ev)` returns a non-nil error. The wrap is intentional:

- `ErrUnsafeEvidence` is a **programmer error**. It indicates the caller handed the recorder an event that should never have been produced (wrong detail block, oversized summary, ordering violation, etc.). It is not a transient capability failure.
- Programmer errors **must not** pollute the capability status that operators use to decide whether the proxy is healthy. If every unsafe-evidence event degraded status, a bug in the normalizer could cause the proxy to look unhealthy for the rest of its uptime.
- Programmer errors **must** surface to the caller so the bug is fixed; they are never silently dropped.
- The wrap keeps `ErrDisabled`, `ErrDegraded`, and `ErrUnavailable` reserved for transient capability failures, so external classifiers do not have to mix programmer errors with operational health.

If a refactor changes `prepareAppend` to demote `ErrUnsafeEvidence` to a silent no-op, or to degrade status for programmer errors, the recorder-layer tests at every covered guard will fail. This is the intended seal.

## Coverage map

The current four-layer coverage. Use this map when auditing; if a row says **—** and a new guard lands at the core validator, the SDK and recorder columns need attention.

| Guard | SDK | Core | Normalizer | Recorder |
| --- | --- | --- | --- | --- |
| Unknown `Category` | ✓ | ✓ | (n/a: every `From*` method hardcodes a known `Category` — `FromAuthDecision`→`CategoryAuth`, `FromSessionStart`/`FromSessionRecord`→`CategorySession`, `FromAttempt`→`CategoryAttempt`, `FromUsage`/`FromUsageRecord`→`CategoryUsage`, `FromPolicyDecision`→`CategoryPolicy`, `FromAudit`→`CategoryAudit`; see `internal/core/controlplane/normalizer.go` line per method. `IsKnown() == false` for bogus `Category("bogus")` locked at `pkg/lipsdk/controlplane/types.go:198-200`.) | [see Unknown `Visibility` row](#unknown-visibility-row) |
| Zero `OccurredAt` | ✓ | ✓ | ✓ `TestNormalizeRejectsZeroTimestamps` | ✓ `TestRecorderRejectsZeroTimestampsAsUnsafeEvidence` |
| Zero `RecordedAt` | ✓ | ✓ | ✓ `TestNormalizeRejectsZeroTimestamps` | ✓ `TestRecorderRejectsZeroTimestampsAsUnsafeEvidence` |
| `RecordedAt` before `OccurredAt` | ✓ | ✓ | ✓ `TestNormalizerEventWithRecordedAtBeforeOccurredAt` | ✓ `TestRecorderRejectsRecordedAtBeforeOccurredAtAsUnsafeEvidence` |
<a id="unknown-visibility-row"></a>
| Unknown `Visibility` | ✓ `TestVisibilityConstantsAreStable` | ✓ | (n/a: `baseEvent` falls back `""`→`VisibilityDefault`/`EvidenceRecorded`/`RedactionNone`; `FromPolicyDecision` priv-maps to known `VisibilityPrivileged`+`RedactionPrivileged`+`EvidenceRedacted`; `FromAudit` priv-maps similarly — all known values; see `internal/core/controlplane/normalizer.go:481-491`. `IsKnown() == false` for bogus `Visibility(\"bogus\")` locked at `pkg/lipsdk/controlplane/types.go:204-206`.) | (n/a: pure SDK enum IsKnown() bound; redundant at recorder layer) |
| Unknown `EvidenceState` | ✓ `TestEvidenceStateConstantsAreStable` | ✓ | (n/a: same `baseEvent` fallback + `FromPolicyDecision`/`FromAudit` priv-mapping path; see [Unknown `Visibility` row](#unknown-visibility-row). `IsKnown() == false` for bogus `EvidenceState("bogus")` locked at `pkg/lipsdk/controlplane/types.go:207-209`.) | [see Unknown `Visibility` row](#unknown-visibility-row) |
| Unknown `RedactionState` | ✓ `TestRedactionStateConstantsAreStable` | ✓ | (n/a: same `baseEvent` fallback + `FromPolicyDecision`/`FromAudit` priv-mapping path; see [Unknown `Visibility` row](#unknown-visibility-row). `IsKnown() == false` for bogus `RedactionState("bogus")` locked at `pkg/lipsdk/controlplane/types.go:210-212`.) | [see Unknown `Visibility` row](#unknown-visibility-row) |
| Privileged visibility w/o privileged redaction | ✓ `TestEventValidateRejectsPrivilegedVisibilityWithoutPrivilegedRedaction` | ✓ | ✓ `TestNormalizerEventWithPrivilegedVisibilityWithoutPrivilegedRedaction` | ✓ `TestRecorderRejectsPrivilegedVisibilityWithoutPrivilegedRedactionAsUnsafeEvidence` |
| Multiple detail blocks | ✓ `TestEventRequiresExactlyOneDetail` | ✓ | ✓ `TestNormalizerEventWithMultipleDetailBlocksRejected` | ✓ `TestRecorderRejectsMultipleDetailBlocksAsUnsafeEvidence` |
| Zero detail blocks | ✓ `TestEventRequiresExactlyOneDetail` | ✓ | (n/a: every `From*` method sets exactly one Detail struct on its output event — structural invariant locked by `TestNormalizeEachCategoryProducesExactlyOneDetail` in `normalizer_test.go`) | ✓ `TestRecorderRejectsZeroDetailBlocksAsUnsafeEvidence` |
| Category-detail mismatch | ✓ `TestEventRequiresExactlyOneDetail`, `TestEventRejectsCategoryDetailMismatch` | ✓ | (n/a: each `From*` pairs a hardcoded `Category` with its matching Detail struct — `CategoryAuth`↔`AuthDetail`, `CategorySession`↔`SessionDetail`, `CategoryAttempt`↔`AttemptDetail`, `CategoryUsage`↔`UsageDetail`, `CategoryPolicy`↔`PolicyDetail`, `CategoryAudit`↔`AuditDetail`) | (n/a: recorder receives events with structurally matched Category and Detail under normal use, since the normalizer is the only producer) |
| Empty `source.name` | — | ✓ | ✓ `TestNormalizerEventWithEmptySourceNameRejected` | ✓ `TestRecorderRejectsEmptySourceNameAsUnsafeEvidence` |
| Oversized `source.name` | — | ✓ | (n/a: normalizer forces `SourceRef` from constructor) | (n/a: recorder reuses core validator) |
| Unsafe summary (token-like) | — | ✓ | ✓ `TestNormalizerEventWithUnsafeSummaryRejected` | ✓ `TestRecorderRejectsUnsafeSummaryAsUnsafeEvidence` |
| Oversized summary | — | ✓ | ✓ `TestNormalizerEventWithOversizedSummaryRejected` | ✓ `TestRecorderRejectsOversizedSummaryAsUnsafeEvidence` |
| Oversized scope map (`Roles` / `SafeClaims` / `PolicyLabels`) | — | ✓ | ✓ `TestNormalizerEventWithOversizedScopeLabelsRejected` | ✓ `TestRecorderRejectsOversizedScopeMapAsUnsafeEvidence` |

`—` means the guard does not exist at that layer. `(n/a)` means the guard cannot fire at that layer under normal use — for example, the normalizer never produces zero detail blocks, so a normalizer-layer test would only exercise the same validation reuse without adding coverage. `(covered via base event)` means the existing per-layer setup or chain exercises the guard implicitly through the shared base event. `(gap: …)` means the guard exists in source but is not pinned by a regression test at that layer — a row marked this way is an audit gap that should be closed before claiming four-layer coverage of the guard. Natural-language justifications for `(n/a)` state *why* the layer cannot fire (e.g., the normalizer forces its inputs from the constructor, so the guard is unreachable from a normalizer-layer test).

When invoking the audit, resolve gaps by adding tests at the missing layer using the templates above. Cross-check new test names against this map and update the row.

## See also

- `docs/architecture-guardrails.md` — overall guardrail philosophy and how the guardrail tests enforce the boundary.
- `docs/core-boundaries.md` — classifies `internal/core/controlplane` as a **state seam** for control-plane validation, status, query, retention, and event ledger.
- `docs/enterprise-extension-boundaries.md` — control-plane `Store`, `QueryService`, `RetentionController`, and `Recorder` are stable enterprise seams.
- `internal/testkit/conformance` — conformance suite that exercises the recorder and core validator in a full pipeline; useful for catching integration-level regressions after a four-layer-coverage guard addition.
