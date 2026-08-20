---
name: golang-error-handling
description: "Create, wrap, classify, expose, log, and test Go errors using errors.Is/As, %w, errors.Join, custom types, sentinel values, panic boundaries, and structured logging. Use when implementing or reviewing error paths and public error contracts."
---

# Go error handling

An error is both control flow and a contract. Preserve the information callers need while keeping internal details out of public responses.

## Create and wrap

- Return `nil` only when the operation succeeded. Use `errors.New` for stable sentinel values and `fmt.Errorf("operation: %w", err)` to add context while preserving identity.
- Define a custom error type when callers need structured fields or a stable `errors.As` target. Keep messages useful to humans but do not make callers parse them.
- Use `errors.Is` for sentinels and `errors.As` for types. `errors.Join` is appropriate when multiple independent failures must be retained; document precedence when a caller needs one primary cause.
- Preserve cancellation and deadline classification through wrapping.

```go
var ErrNotFound = errors.New("not found")

func load(ctx context.Context, id string) (*Item, error) {
    item, err := store.Get(ctx, id)
    if err != nil {
        return nil, fmt.Errorf("load item %q: %w", id, err)
    }
    return item, nil
}
```

## Handle once, expose deliberately

At each layer choose the owner of handling: classify/translate, log, or return. Avoid logging the same error at every stack frame. A layer may add structured context and return; the boundary that has the right audience should log or map it.

`%v` versus `%w` is not a security boundary. Formatting changes text and `%v` loses wrapping, but neither guarantees that a message is safe to send to a client. Map internal errors explicitly to a public status/code/message, log the detailed cause with access controls, and keep secrets and user input out of logs unless redacted.

Use panic only for programmer invariants or initialization failures that cannot be represented as an error. Recover at a deliberate goroutine or server boundary, convert the value to an error, preserve the stack in internal diagnostics, and ensure the process does not continue with corrupt state. Do not use `recover` to hide ordinary errors.

## Verification

Test success, sentinel/type classification, joined errors, cancellation, and public mapping. Check every ignored error and every `defer` cleanup error for an intentional policy. See [creation](references/error-creation.md), [handling](references/error-handling.md), and [wrapping](references/error-wrapping.md).
