---
type: reference
title: Control-plane evidence four-layer guard regression pattern
description: Audit property requiring every control-plane evidence guard to be regression-locked at all four layers — SDK (Event.Validate), core validator (ValidateEvent), normalizer (From*), and recorder (prepareAppend).
stack: [go]
tags: [controlplane, testing, audit, regression, okf]
status: active
---

# Control-plane evidence four-layer guard regression pattern

Every control-plane evidence record must be regression-locked at four layers: SDK guard (`pkg/lipsdk/controlplane.Event.Validate`), core validator (`internal/core/controlplane.ValidateEvent`), normalizer (`From*` methods), and recorder (`prepareAppend` → `ErrUnsafeEvidence` wrapper). A future refactor that weakens any single layer is caught by the test at that layer.

For the full audit-property prose, mutation rules, Go templates per layer, and the current per-guard coverage map, see [`docs/controlplane-evidence.md`](../../docs/controlplane-evidence.md) — that file is the source of truth for the audit property. The cross-reference in `docs/architecture-guardrails.md`'s PR checklist routes contributors encountering a new control-plane guard here before they start implementation.

## Source priority

When this page, `docs/controlplane-evidence.md`, and the implementation disagree, the implementation wins. Update this page and `docs/controlplane-evidence.md` together with the implementation so the audit property stays convergent.
