---
name: golang-testing
description: Write, structure, review, and optimize Go tests and benchmarks: table-driven unit tests, testify assertions and mocks, integration testing, concurrency testing, fuzzing, and benchmark profiling with b.Loop and benchstat.
---

# Go Testing & Benchmarking Guide

Testing in Go follows a Test-Driven Development (TDD) philosophy: write test interfaces and assertions first, implement the smallest correct diff second, and verify with race and quality gates.

---

## 1. Table-Driven Unit Testing

Table-driven tests are the standard idiom for testing multiple inputs and edge cases cleanly.

~~~go
func TestParseEndpoint(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name        string
        input       string
        wantHost    string
        wantPort    int
        wantErrCls  error
    }{
        {
            name:       "valid host and port",
            input:      "localhost:8080",
            wantHost:   "localhost",
            wantPort:   8080,
            wantErrCls: nil,
        },
        {
            name:       "missing port",
            input:      "localhost",
            wantErrCls: ErrInvalidFormat,
        },
    }

    for _, tc := range tests {
        tc := tc // pin variable for parallel subtests
        t.Run(tc.name, func(t *testing.T) {
            t.Parallel()

            host, port, err := ParseEndpoint(tc.input)
            if tc.wantErrCls != nil {
                require.ErrorIs(t, err, tc.wantErrCls)
                return
            }
            require.NoError(t, err)
            assert.Equal(t, tc.wantHost, host)
            assert.Equal(t, tc.wantPort, port)
        })
    }
}
~~~

### Test Lifecycle & Helpers
- **`t.Helper()`**: Always mark helper functions with `t.Helper()` so failure line numbers point to the test call site rather than the helper.
- **`t.Cleanup()`**: Register teardown logic (closing listeners, stopping test servers, releasing resources) immediately after initialization.
- **`t.TempDir()` & `t.Setenv()`**: Use built-in test runners for isolated filesystem and environment manipulation; they auto-cleanup upon test completion.

---

## 2. Assertions with `stretchr/testify`

Use `testify` to write concise, diagnostic assertions:

### `require` vs `assert`
- Use **`require.*`** for preconditions where proceeding further would panic or produce confusing cascading errors (e.g., `require.NoError(t, err)`, `require.NotNil(t, result)`).
- Use **`assert.*`** for domain output validations where seeing multiple failing assertions across a test execution is valuable.

~~~go
import (
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestUserCreation(t *testing.T) {
    user, err := CreateUser(ctx, "alice@example.com")
    require.NoError(t, err, "user creation must succeed")
    require.NotNil(t, user)

    assert.Equal(t, "alice@example.com", user.Email)
    assert.True(t, user.CreatedAt.Before(time.Now()))
}
~~~

### Asynchronous & Eventual Assertions
Avoid flaky `time.Sleep()` in async tests. Use `assert.Eventually` or `require.EventuallyWithT`:
~~~go
require.EventuallyWithT(t, func(c *assert.CollectT) {
    status := service.GetStatus()
    assert.Equal(c, StatusReady, status)
}, 5*time.Second, 50*time.Millisecond, "service did not become ready in time")
~~~

---

## 3. Mocks & Test Doubles

### Hand-Rolled Function Stubs vs Testify Mocks
- Prefer **small interface stubs or function types** for simple dependencies:
~~~go
type stubStore struct {
    saveFunc func(ctx context.Context, item Item) error
}
func (s stubStore) Save(ctx context.Context, item Item) error {
    return s.saveFunc(ctx, item)
}
~~~
- Use `testify/mock` when call counts, argument matching, or strict interaction sequences must be asserted:
~~~go
type MockNotifier struct {
    mock.Mock
}

func (m *MockNotifier) Notify(ctx context.Context, msg string) error {
    args := m.Called(ctx, msg)
    return args.Error(0)
}

func TestWorkflow(t *testing.T) {
    notifier := new(MockNotifier)
    notifier.On("Notify", mock.Anything, "welcome").Return(nil).Once()

    RunWorkflow(ctx, notifier)
    notifier.AssertExpectations(t)
}
~~~

---

## 4. Benchmarking & Profiling

### Modern Go Benchmark Loop (`b.Loop`)
In Go >= 1.24, prefer `b.Loop()` which automatically handles warmup, timer resets, and iteration bounds:
~~~go
func BenchmarkProcessPayload(b *testing.B) {
    payload := generateTestPayload(1024)
    b.ReportAllocs()

    for b.Loop() {
        _ = ProcessPayload(payload)
    }
}
~~~

### Classic Benchmark Loop (Go < 1.24)
~~~go
func BenchmarkProcessPayloadLegacy(b *testing.B) {
    payload := generateTestPayload(1024)
    b.ReportAllocs()
    b.ResetTimer()

    for i := 0; i < b.N; i++ {
        _ = ProcessPayload(payload)
    }
}
~~~

### Benchmark Comparison with `benchstat`
To measure performance impact objectively without noise:
```bash
# Capture baseline
git checkout main
go test -bench=BenchmarkProcessPayload -count=10 ./pkg/... > old.txt

# Capture candidate
git checkout feat/optimization
go test -bench=BenchmarkProcessPayload -count=10 ./pkg/... > new.txt

# Statistical comparison
benchstat old.txt new.txt
```

### Capturing Profiles from Tests
```bash
go test -bench=BenchmarkProcessPayload -cpuprofile=cpu.pprof -memprofile=mem.pprof ./pkg/...
go tool pprof -http=:8080 cpu.pprof
```

---

## 5. Fuzz Testing

Use native Go fuzzing to uncover edge-case panics, boundary violations, and parser vulnerabilities:
~~~go
func FuzzDecodeMessage(f *testing.F) {
    // Seed corpus
    f.Add([]byte(`{"type":"ping"}`))
    f.Add([]byte(`{"type":"data","payload":"hello"}`))

    f.Fuzz(func(t *testing.T, data []byte) {
        msg, err := DecodeMessage(data)
        if err != nil {
            return // Rejecting invalid input is expected
        }
        // Validate invariants on decoded message
        require.NotEmpty(t, msg.Type)
    })
}
~~~
Run fuzzing with:
```bash
go test -fuzz=FuzzDecodeMessage -fuzztime=30s ./pkg/...
```

---

## 6. Testing Verification Checklist

- [ ] Every new feature or bug fix has an accompanying regression test.
- [ ] Tests run cleanly under `go test -race ./...`.
- [ ] No race-prone `time.Sleep` calls; synchronization uses channels, wait groups, or `assert.Eventually`.
- [ ] Test fixtures and mock servers always clean up via `t.Cleanup`.
