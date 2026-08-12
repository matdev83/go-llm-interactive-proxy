# `lipruntime.Options` registration guidance

`pkg/lipruntime.Options` exposes only the canonical non-money authority
registration seams used by runtime admission and coordination:

- `RequestRegistrations`
- `AttemptRegistrations`
- `ConcurrencyRegistration`

Legacy parallel provider fields were removed during the alpha-stage architecture
convergence. Monetary rating, customer settlement, and provider-cost accounting
are not runtime registration concerns; they are owned by post-turn
`internal/core/billing` and its durable composition adapters.

Example request-authority registration:

```go
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

Do not attach a stream-time rater or monetary ledger to the runtime. Provide
billing policy, immutable pricing/rate snapshots, and durable billing ports at
the billing composition boundary instead.
