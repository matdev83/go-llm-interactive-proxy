---
name: golang-error-handling
description: Handle, wrap, classify, and format Go errors idiomatically: errors.Is/As, %w wrapping, errors.Join, structured errors with samber/oops, domain-to-wire error mapping, and panic boundaries.
---

# Go Error Handling Guide

Go treats errors as regular values. Errors must be checked explicitly, contextualized as they travel up the call stack, and translated cleanly into protocol-appropriate shapes at API boundaries.

---

## 1. Standard Error Inspection & Wrapping

### Wrapping with `%w`
Use `fmt.Errorf` with `%w` to wrap an underlying error while preserving its identity for inspection:

~~~go
func ReadConfig(path string) (*Config, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("read config file %q: %w", path, err)
    }
    return parse(data)
}
~~~

### Inspecting with `errors.Is` & `errors.As`
- **`errors.Is(err, target)`**: Checks if any error in the wrap chain matches the target sentinel value:
~~~go
if errors.Is(err, os.ErrNotExist) {
    // handle missing file
}
~~~

- **`errors.As(err, &target)`**: Finds the first error in the chain that matches the target type and assigns it:
~~~go
var pathErr *os.PathError
if errors.As(err, &pathErr) {
    log.Printf("failed on path: %s", pathErr.Path)
}
~~~

### Combining Multiple Errors (`errors.Join`)
Use `errors.Join` when coordinating multiple operations (e.g., closing multiple resources or accumulating validation errors):
~~~go
func (c *Client) Close() error {
    return errors.Join(
        c.conn.Close(),
        c.tracer.Shutdown(context.Background()),
        c.metrics.Flush(),
    )
}
~~~

---

## 2. Sentinels vs Custom Error Types

| Strategy | When to Use | Example |
| :--- | :--- | :--- |
| **Sentinel Values** | Fixed error conditions where no dynamic metadata is needed. | `var ErrNotFound = errors.New("not found")` |
| **Custom Structs** | Errors requiring structured contextual fields (e.g., field name, code). | `type ValidationError struct { Field string; Msg string }` |

~~~go
type ValidationError struct {
    Field   string
    Message string
}

func (e *ValidationError) Error() string {
    return fmt.Sprintf("validation failed on %s: %s", e.Field, e.Message)
}
~~~

---

## 3. Structured Errors with `samber/oops`

For complex internal domain logic where error codes, attributes, user messages, and stack traces must be attached cleanly, use `samber/oops`:

~~~go
import "github.com/samber/oops"

func ProcessPayment(ctx context.Context, userID string, amount int64) error {
    if amount <= 0 {
        return oops.
            Code("invalid_amount").
            With("user_id", userID).
            With("amount", amount).
            User("Amount must be greater than zero.").
            Errorf("payment processing rejected: non-positive amount")
    }

    if err := chargeCard(ctx, userID, amount); err != nil {
        return oops.
            Code("card_charge_failed").
            With("user_id", userID).
            Wrapf(err, "failed to charge card")
    }
    return nil
}
~~~

---

## 4. Domain-to-Wire Error Translation

Never leak internal database errors, raw SQL queries, or third-party stack traces directly to external API callers. Map domain errors explicitly at the HTTP or gRPC boundary:

~~~go
func WriteHTTPError(w http.ResponseWriter, err error) {
    var status int
    var clientMsg string

    switch {
    case errors.Is(err, ErrNotFound):
        status = http.StatusNotFound
        clientMsg = "Resource not found"
    case errors.Is(err, ErrUnauthorized):
        status = http.StatusUnauthorized
        clientMsg = "Authentication required"
    case errors.Is(err, ErrInvalidInput):
        status = http.StatusBadRequest
        clientMsg = err.Error()
    default:
        // Mask unexpected internal failures
        status = http.StatusInternalServerError
        clientMsg = "Internal server error"
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(map[string]string{"error": clientMsg})
}
~~~

---

## 5. Panic Recovery Boundaries

Panics must be caught and contained at the top-level boundary (e.g., HTTP middleware or background worker root) to prevent crashing the server:

~~~go
func RecoveryMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if rec := recover(); rec != nil {
                slog.ErrorContext(r.Context(), "panic recovered in HTTP handler",
                    slog.Any("panic", rec),
                    slog.String("stack", string(debug.Stack())),
                )
                http.Error(w, "Internal Server Error", http.StatusInternalServerError)
            }
        }()
        next.ServeHTTP(w, r)
    })
}
~~~
