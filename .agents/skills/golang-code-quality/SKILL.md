---
name: golang-code-quality
description: Enforce Go code style, idiomatic naming, safety bug prevention (nil interfaces, slice aliasing, lock copies), linter configuration (golangci-lint), and behavior-preserving code simplification.
---

# Go Code Quality & Safety Guide

Writing high-quality Go code requires adherence to standard formatting conventions, defensive programming against language-specific runtime traps, consistent naming, and rigorous static analysis.

---

## 1. Idiomatic Naming Conventions

Go names should be concise, predictable, and context-aware:

### Package Names
- Short, lowercase, singular nouns (e.g., `transport`, `config`, `auth`).
- Avoid generic junk names like `util`, `common`, `helpers`, or `base`.
- Never repeat the package name in exported identifiers (e.g., `auth.Service`, not `auth.AuthService`; `json.Encoder`, not `json.JSONEncoder`).

### Receivers, Variables, & Interfaces
- **Receiver Names**: 1–2 letters derived from the type name (e.g., `func (s *Server) Start()`). Be consistent across all methods on that type. Never use `this` or `self`.
- **Interface Names**: Single-method interfaces end in `-er` (e.g., `Reader`, `Writer`, `Formatter`). Multi-method interfaces describe a role or capability (e.g., `Tokenizer`, `Authenticator`).
- **Error Sentinels & Types**:
  - Sentinels begin with `Err` (e.g., `ErrNotFound`, `ErrTimeout`).
  - Custom error structs end with `Error` (e.g., `ValidationError`, `NetworkError`).

---

## 2. Idiomatic Code Style & Control Flow

### Guard Clauses & Early Returns
Minimize cognitive nesting by handling errors and edge cases early:
~~~go
// GOOD: Early return with minimal indentation
func ProcessOrder(order *Order) error {
    if order == nil {
        return ErrNilOrder
    }
    if err := validateOrder(order); err != nil {
        return fmt.Errorf("invalid order: %w", err)
    }
    return executePayment(order)
}
~~~

### Zero-Value Semantics
- **Slices**: A `nil` slice has length and capacity 0 and is completely safe to `append` to and `range` over. Only allocate with `make([]T, 0, cap)` when preallocation is beneficial.
- **Maps**: A `nil` map is safe to read and `range` over (returns zero value for missing keys), but writing to a `nil` map panics. Always initialize maps before writing (`make(map[K]V)`).

---

## 3. Go Safety Traps & Defensive Coding

### Trap 1: The Nil Interface Pitfall
An interface value is `nil` only if both its type and its value are `nil`. Storing a typed `nil` pointer into an interface makes the interface **non-nil**:

~~~go
type CustomError struct{}
func (e *CustomError) Error() string { return "custom error" }

func Run() error {
    var err *CustomError = nil
    if condition {
        err = &CustomError{}
    }
    return err // BUG: returns a non-nil interface containing a nil *CustomError!
}

// FIX: Explicitly return nil
func RunSafe() error {
    if condition {
        return &CustomError{}
    }
    return nil
}
~~~

### Trap 2: Slice Aliasing & Mutation
Sub-slicing (`s[a:b]`) shares the underlying array with the original slice. Modifying elements or appending when capacity permits will mutate the original array:

~~~go
// Prevent unintended aliasing using full slice expression (3-index slice)
limited := original[0:5:5] // caps capacity at 5, forcing append to reallocate
~~~

### Trap 3: Copying Mutexes
Mutexes must never be copied after initialization. Always use pointer receivers for structs containing `sync.Mutex`, `sync.RWMutex`, or `sync.WaitGroup`.

### Trap 4: Integer Conversions & Overflow
Go does not prevent numeric overflow on explicit type conversions (e.g., `int64` to `int32`). Check bounds before casting if inputs originate from untrusted external sources.

---

## 4. Linting & Static Analysis

Maintain clean static analysis using `golangci-lint`:

### Recommended Linters Configuration (`.golangci.yml`)
~~~yaml
linters:
  enable:
    - govet         # Standard compiler vet checks
    - staticcheck   # Advanced Go static analysis
    - errcheck      # Unchecked error returns
    - ineffassign   # Ineffective variable assignments
    - revive        # General Go style and naming linter
    - gosec         # Security scanner
    - unconvert     # Unnecessary type conversions
    - unused        # Unused constants, variables, functions, and types
~~~

### Linter Suppressions
When a warning is a deliberate, documented exception, suppress it locally with an explanatory comment:
~~~go
//nolint:gosec // Safe: buffer size is bounded by MAX_HEADER_SIZE check above
h := sha1.New()
~~~

---

## 5. Behavior-Preserving Simplification

When refactoring:
- Use standard library functions where available (e.g., `strings.Cut` instead of `strings.SplitN`, `slices.Contains` instead of manual loops).
- Eliminate dead code, unused parameters, and redundant variable assignments.
- Never alter public function signatures or exported behaviors during a pure simplification pass.
