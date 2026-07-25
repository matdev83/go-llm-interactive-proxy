# Legacy `lipruntime.Options` field migration

Concise field-by-field map for callers that still reference the removed parallel
provider/rater fields on public `pkg/lipruntime.Options`.

## Alpha-stage decision

- **Decision date:** 2026-07-25
- **Approving maintainer:** matdev83
- **Context:** the repository is alpha with no users. No supported stable
  release or user contract depended on the legacy fields. Removal proceeded in
  this convergence instead of waiting for an unspecified future major version.
- **Canonical model:** descriptor-bound registrations only —
  `RequestRegistrations`, `AttemptRegistrations`, `ConcurrencyRegistration`,
  `RaterRegistrations` (descriptor embedded on each registration).

## Field-by-field migration

| Removed field | Replacement |
| --- | --- |
| `RequestProviders` + request descriptors | `RequestRegistrations` |
| `AttemptProviders` + attempt descriptors | `AttemptRegistrations` |
| `ConcurrencyProvider` + lease descriptor | `ConcurrencyRegistration` |
| `Rater` | `RaterRegistrations` |
| `ProviderDescriptors` | descriptor embedded in each registration |

## Commit / release markers

| Marker | SHA / note |
| --- | --- |
| Last commit containing the five legacy fields | `33339e79` (`docs(runtime): publish legacy options migration contract`) |
| First commit with registration-only `Options` | `e9a5d507` (`refactor(runtime): remove legacy public options`) |
| First release with only canonical registrations | any release cut from `e9a5d507` or later (alpha; no prior stable user contract on the removed fields) |

The quarantined `pkg/lipruntime/legacy_options.go` adapter and the older
“current major only” Options migration guide are deleted and must not be restored.

## Example (request authority)

```go
// Removed (no longer compiles)
opts := lipruntime.Options{
    RequestProviders: []authority.RequestProvider{myRequest{}},
    ProviderDescriptors: []authority.ProviderDescriptor{{
        ID: "quota", Kind: authority.ProviderKindAuthority,
        Postures: []authority.StagePosture{{
            Stage: authority.StageRequestAdmit,
            Strength: authority.StrengthRequired,
            FailureBehavior: authority.FailureFailClosed,
        }},
    }},
}

// Canonical
opts := lipruntime.Options{
    RequestRegistrations: []authority.RequestRegistration{{
        Descriptor: authority.ProviderDescriptor{
            ID: "quota", Kind: authority.ProviderKindAuthority,
            Postures: []authority.StagePosture{{
                Stage: authority.StageRequestAdmit,
                Strength: authority.StrengthRequired,
                FailureBehavior: authority.FailureFailClosed,
            }},
        },
        Priority: authority.RequestPriorityQuotaBudgetRate,
        Provider: myRequest{},
    }},
}
```
