---
name: golang-samber-oops
description: "Use and review samber/oops cause wrapping, error classification, attributes, public mapping, stack traces, builder reuse, and recovery boundaries."
---

# samber/oops

This guidance targets github.com/samber/oops v1.23. oops errors implement error and preserve an underlying cause through Unwrap. It complements, rather than replaces, ordinary errors.Is/As and deliberate public error mapping.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Trace the original cause through nested Wrap, nil passthrough, errors.Is/As, classification, public mapping, and terminal logging.
- Inspect With/WithContext/User/Tenant fields for secrets, retained mutable values, unsafe public serialization, and unbounded diagnostic cardinality.
- Check builder reuse semantics against the pinned source, context-key types, stacktrace placement, and field precedence across nested errors.
- Follow recovered panics in the same goroutine and verify state remains valid; public messages are deliberate fields, not automatically safe Error() strings.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Build and wrap

The builder is immutable-style and materializes an error at terminal methods:

~~~go
func CreateUser(ctx context.Context, id string) (err error) {
    if err := validateID(id); err != nil {
        return oops.Code("validation").With("user_id", id).Wrap(err)
    }
    return oops.In("users").
        WithContext(ctx, "request_id").
        Code("create_failed").
        Public("Unable to create the user").
        Wrap(save(ctx, id))
}
~~~

Use With with key/value pairs and keep secrets/large payloads out of attributes. Use Code for machine classification, In for a domain, Trace/Span for correlation, and Public for a user-safe message. Attributes improve logs and diagnostics; they are not a license to record personal data.

Use oops.Wrap or a builder's Wrap for a causal error. Do not replace a sentinel or typed cause if callers need errors.Is/As. Map an internal oops error to a transport status in the boundary; percent-v formatting does not sanitize or establish an API security boundary.

## Recovery

Recover and Recoverf accept a callback with no return values and return an error:

~~~go
func runSafely() (err error) {
    return oops.Code("panic_recovered").Recover(func() {
        work()
    })
}
~~~

Use recovery at a process/request boundary where the policy is to convert a panic. Do not wrap every function or continue after an invariant panic without understanding state corruption. Ensure the callback captures the intended values; there is no implicit variable named r.

## Inspection and logging

Use oops.AsOops(err) according to the installed API, or errors.As when the concrete target is appropriate. OopsError exposes Code, Public, Context, Stacktrace, Sources, and LogValue-style data in current releases. Check the exact accessor signatures before writing adapters.

Log detailed diagnostics once at the ownership boundary. Return/wrap lower-level errors with operation context. Redact attributes, request/response bodies, authorization data, and identifiers according to the application's privacy policy. Test cause identity, code/public fields, nested wrapping, recovery, and serialization if the error crosses a wire.
