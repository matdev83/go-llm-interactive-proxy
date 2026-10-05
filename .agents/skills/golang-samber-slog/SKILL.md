---
name: golang-samber-slog
description: "Compose and review log/slog handlers with samber/slog packages: routing, fanout/failover, sampling, redaction, middleware, backend contracts, and flush/close."
---

# samber/slog packages

Use log/slog as the stable contract and verify each github.com/samber/slog-* module/version before copying a constructor. A handler is synchronous from the caller's perspective unless a specific backend documents buffering/async behavior; own its flush and close lifecycle.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Trace actual handler order through enrichment, redaction, sampling, routing, fanout/failover, and sinks, including overlapping predicates and failed sinks.
- Check Enabled, Handle, WithAttrs, WithGroup, context propagation, record/attribute mutation, and concurrency against the slog.Handler contract.
- Inspect queues, backpressure, flush/close, sampling of required events, and failure recursion; a fallback sink can amplify or hide an outage.
- Assert redaction on every branch and actual emitted structured output. Verify constructor/API names against each pinned backend module rather than a generic slog package model.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Composition

Choose the topology that matches the requirement:

- Fanout/broadcast sends one record to every selected handler; it multiplies work and latency.
- Router predicates select handlers; a record may reach several handlers if predicates overlap.
- Pool selects a handler and tries eligible handlers sequentially with fall-through on error. It is load balancing/failover, not broadcast and not parallel execution.
- Pipe applies middleware-style transformations to one downstream handler.

Pool's Enabled checks child handlers and Handle chooses an eligible handler; it does not guarantee that every destination receives every record. Test the failure policy, ordering, and whether a failed sink should be skipped, retried, or surfaced.

Sampling applies to records selected by the sampling handler. It does not automatically exempt errors just because they have a high level; configure a predicate/level policy explicitly and keep a separate unsampled security/audit sink when required. Sampling before expensive formatting can reduce cost, but never sample away events required by compliance or incident response.

A useful conceptual pipeline is context/enrichment, redaction, level routing, sampling policy, formatting, then sinks. The actual package constructors and handler order must match the pinned release.

## Context and privacy

Use slog attributes and LogValuer for structured values. Request IDs and trace IDs are linkable identifiers; treat them as potentially personal data and minimize retention. Redact secrets, tokens, credentials, raw bodies, and unnecessary identifiers before fanout. Do not assume self-hosting removes privacy obligations.

HTTP middleware should record method, route template, status, and duration, not arbitrary request bodies. Install a framework-specific middleware only when it can obtain a bounded route and request context. Make health/metrics route filtering explicit.

## Backends and lifecycle

Backend modules have different constructor contracts. For a Datadog or other client-backed handler, construct the required client with its documented options, pass that client to the handler, and close/flush it according to the package. Do not present a zero-argument constructor as portable. Network backends need bounded queues, timeout, retry, drop/backpressure policy, and shutdown flush behavior.

Test handlers with an in-memory slog.Handler, assert Enabled and Handle behavior, and exercise WithAttrs/WithGroup. Verify metric/log field names against emitted output rather than stale dashboards. Keep logging failures observable without causing an outage unless the product explicitly treats logs as a required side effect.
