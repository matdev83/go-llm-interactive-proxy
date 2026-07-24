# Public `lipruntime.Options` migration guide

This guide maps every deprecated public `pkg/lipruntime.Options` field to its
canonical registration replacement. It is the public migration contract for
Task 8.3 (requirements 10.7–10.10, 12.8).

## Status and removal schedule

- **Preferred path (now):** descriptor-bound registrations —
  `RequestRegistrations`, `AttemptRegistrations`, `ConcurrencyRegistration`,
  and `RaterRegistrations`.
- **Legacy fields (current major only):** `RequestProviders`,
  `AttemptProviders`, `ConcurrencyProvider`, `Rater`, and
  `ProviderDescriptors` remain source-compatible only for the current major
  line. They are quarantined in `pkg/lipruntime/legacy_options.go` and are
  not part of internal Host construction.
- **Final legacy-support release:** the last release in the current major line before the next compatible major-version boundary.
- **Next compatible major deletion target:** at the next compatible major
  boundary, delete `RequestProviders`, `AttemptProviders`,
  `ConcurrencyProvider`, `Rater`, and `ProviderDescriptors`, the public
  legacy adapter, descriptor family/stage filtering, and the compatibility ID
  `legacy-production-rater` (and related legacy pairing/normalization tests).

No new feature, option, provider class, stage family, or validation rule may
be added to the legacy option model in the current major.

## Field-by-field migration

| Deprecated field(s) | Canonical replacement | Notes |
| --- | --- | --- |
| `RequestProviders` + request-stage `ProviderDescriptors` | `RequestRegistrations []authority.RequestRegistration` | Embed the descriptor on each registration; set `Priority` and `Provider`. Exact cardinality pairing in legacy mode; never invent `production-request-%d` IDs. |
| `AttemptProviders` + attempt-stage `ProviderDescriptors` | `AttemptRegistrations []authority.AttemptRegistration` | Same pairing rules; never invent `production-attempt-%d` IDs. |
| `ConcurrencyProvider` + one lease-stage `ProviderDescriptor` | `ConcurrencyRegistration *authority.ConcurrencyRegistration` | Legacy requires exactly one lease-stage descriptor. |
| `Rater` | `RaterRegistrations []economics.RaterRegistration` | Set explicit `ID` and `Perspective` (`metering.PerspectiveOperator` when that is the intended view). Legacy alone maps to ID `legacy-production-rater` with operator perspective **compatibility-only** — do not copy that ID into new code. |
| `ProviderDescriptors` | Descriptors embedded on the registrations above | Observer-only descriptors remain a current-major compatibility path only when paired with observer options; they must not be used for new authority binding. |

## Compatibility decisions (current major)

- **Do not mix** canonical registrations and the corresponding legacy fields on
  the same `Options` value (`RequestProviders` with `RequestRegistrations`,
  and likewise for attempt, concurrency, and rater).
- **Do not invent** `production-request-%d` / `production-attempt-%d`
  identities; legacy pairing uses caller-supplied descriptor IDs only.
- **Descriptor cardinality** for legacy conversion remains exact (1:1 for
  request/attempt providers; exactly one lease-stage descriptor for
  concurrency).
- **Canonical construction below the public adapter is registration-only.**
  `runtimebundle.ProductionOptions` and Host construction do not accept the
  five deprecated fields.
- **Legacy stage families are frozen** to request, attempt, and lease posture
  families already documented here. No new provider class may be introduced
  through the legacy adapter.

## Before / after examples

Examples are short and compilable-looking; replace providers/raters with your
implementations. No secrets or config values are shown.

### Request authority

```go
// Before (legacy, current major only)
opts := lipruntime.Options{
    RequestProviders: []authority.RequestProvider{myRequest{}},
    ProviderDescriptors: []authority.ProviderDescriptor{{
        ID:   "quota",
        Kind: authority.ProviderKindAuthority,
        Postures: []authority.StagePosture{{
            Stage:           authority.StageRequestAdmit,
            Strength:        authority.StrengthRequired,
            FailureBehavior: authority.FailureFailClosed,
        }},
    }},
}

// After (canonical)
opts := lipruntime.Options{
    RequestRegistrations: []authority.RequestRegistration{{
        Descriptor: authority.ProviderDescriptor{
            ID:   "quota",
            Kind: authority.ProviderKindAuthority,
            Postures: []authority.StagePosture{{
                Stage:           authority.StageRequestAdmit,
                Strength:        authority.StrengthRequired,
                FailureBehavior: authority.FailureFailClosed,
            }},
        },
        Priority: authority.RequestPriorityQuotaBudgetRate,
        Provider: myRequest{},
    }},
}
```

### Attempt authority

```go
// Before (legacy)
opts := lipruntime.Options{
    AttemptProviders: []authority.AttemptProvider{myAttempt{}},
    ProviderDescriptors: []authority.ProviderDescriptor{{
        ID:   "spend",
        Kind: authority.ProviderKindAuthority,
        Postures: []authority.StagePosture{{
            Stage:           authority.StageAttemptAdmit,
            Strength:        authority.StrengthRequired,
            FailureBehavior: authority.FailureFailClosed,
        }},
    }},
}

// After (canonical)
opts := lipruntime.Options{
    AttemptRegistrations: []authority.AttemptRegistration{{
        Descriptor: authority.ProviderDescriptor{
            ID:   "spend",
            Kind: authority.ProviderKindAuthority,
            Postures: []authority.StagePosture{{
                Stage:           authority.StageAttemptAdmit,
                Strength:        authority.StrengthRequired,
                FailureBehavior: authority.FailureFailClosed,
            }},
        },
        Priority: authority.AttemptPriorityHardSpend,
        Provider: myAttempt{},
    }},
}
```

### Concurrency / lease

```go
// Before (legacy)
opts := lipruntime.Options{
    ConcurrencyProvider: myLease{},
    ProviderDescriptors: []authority.ProviderDescriptor{{
        ID:   "lease",
        Kind: authority.ProviderKindAuthority,
        Postures: []authority.StagePosture{{
            Stage:           authority.StageLeaseAdmit,
            Strength:        authority.StrengthRequired,
            FailureBehavior: authority.FailureFailClosed,
        }},
    }},
}

// After (canonical)
opts := lipruntime.Options{
    ConcurrencyRegistration: &authority.ConcurrencyRegistration{
        Descriptor: authority.ProviderDescriptor{
            ID:   "lease",
            Kind: authority.ProviderKindAuthority,
            Postures: []authority.StagePosture{{
                Stage:           authority.StageLeaseAdmit,
                Strength:        authority.StrengthRequired,
                FailureBehavior: authority.FailureFailClosed,
            }},
        },
        Provider: myLease{},
    },
}
```

### Rater

```go
// Before (legacy) — maps compatibility-only to ID "legacy-production-rater"
opts := lipruntime.Options{Rater: myRater{}}

// After (canonical) — choose an explicit ID; do not reuse legacy-production-rater
opts := lipruntime.Options{
    RaterRegistrations: []economics.RaterRegistration{{
        ID:          "operator-catalog",
        Perspective: metering.PerspectiveOperator,
        Rater:       myRater{},
    }},
}
```

## Enterprise / external module guidance

External modules (see `testdata/enterprise_module`) should:

- Import only public `pkg/lipruntime` and `pkg/lipsdk/*`.
- Use canonical registrations (`RequestRegistrations`,
  `AttemptRegistrations`, `ConcurrencyRegistration`, `RaterRegistrations`).
- **Must not import `internal/`** packages; those are not a public extension
  seam.

Related boundary docs:

- [enterprise-extension-boundaries.md](enterprise-extension-boundaries.md)
- [runtime-flow.md](runtime-flow.md)
- [extension-platform-authoring.md](extension-platform-authoring.md)
