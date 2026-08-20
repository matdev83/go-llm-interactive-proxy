---
name: golang-ecosystem-libraries
description: Select, evaluate, manage, and utilize popular Go libraries and dependency management: standard library baseline, Go modules hygiene (MVS, govulncheck), and the samber functional toolkit (lo, mo, hot, do, ro, slog).
---

# Go Ecosystem, Libraries & Dependency Management Guide

Go has a rich ecosystem, but codebases should prefer the standard library as the primary baseline. Third-party packages should be selected for API stability, security, minimal transitive dependencies, and active maintenance.

---

## 1. Dependency Management & Go Modules

### Core Principles
- **Standard Library First**: Evaluate stdlib solutions before introducing external dependencies.
- **Minimal Version Selection (MVS)**: Go resolves the lowest compatible version that satisfies requirements rather than blindly pulling the latest release.
- **Auditing & Supply-Chain Security**: Scan dependencies for known CVEs using `govulncheck`.

### Essential Commands
```bash
# Clean unused dependencies and update go.sum
go mod tidy

# Upgrade a specific dependency within semver bounds
go get github.com/samber/lo@latest
go mod tidy

# Audit entire dependency tree for known vulnerabilities
govulncheck ./...

# Verify module integrity against sums
go mod verify
```

---

## 2. The `samber` Functional & Systems Toolkit

The `github.com/samber` ecosystem provides high-quality generic data and functional utilities:

### A. Generic Collections with `samber/lo`
`lo` provides generic map, filter, chunk, and collection helpers:

~~~go
import "github.com/samber/lo"

// Map and Filter
names := []string{"alice", "bob", "alex", "charlie"}
aNames := lo.Filter(names, func(s string, _ int) bool {
    return strings.HasPrefix(s, "a")
})
lengths := lo.Map(names, func(s string, _ int) int {
    return len(s)
})

// Uniq & Grouping
unique := lo.Uniq([]int{1, 2, 2, 3, 3, 3}) // [1, 2, 3]
grouped := lo.GroupBy(users, func(u User) string { return u.Role })
~~~

### B. Option & Result Types with `samber/mo`
`mo` introduces type-safe generic `Option[T]` and `Result[T]` for handling optional values and results explicitly without nil pointers:

~~~go
import "github.com/samber/mo"

func FindUser(id string) mo.Option[User] {
    user, found := database[id]
    if !found {
        return mo.None[User]()
    }
    return mo.Some(user)
}

// Consuming Option
opt := FindUser("123")
if opt.IsPresent() {
    fmt.Println(opt.MustGet().Name)
}
user := opt.OrElse(defaultGuestUser)
~~~

### C. In-Memory Caching with `samber/hot`
`hot` is a generic, bounded, concurrency-safe in-memory cache with TTL and LRU eviction:

~~~go
import (
    "time"
    "github.com/samber/hot"
)

cache := hot.New[string, *UserProfile]().
    Capacity(10_000).
    TTL(10 * time.Minute).
    Build()

cache.Set("user:123", profile)
if p, ok := cache.Get("user:123"); ok {
    // Cache hit
}
~~~

### D. Composing Structured Logging with `samber/slog-multi`
Fan-out or route logs to multiple handlers (e.g., JSON stdout + error-level metrics):

~~~go
import (
    "os"
    "log/slog"
    slogmulti "github.com/samber/slog-multi"
)

logger := slog.New(
    slogmulti.Fanout(
        slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}),
        slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}),
    ),
)
~~~

---

## 3. Dependency Selection Checklist

Before adding a new dependency to `go.mod`:
1. **Transitive Weight**: Check how many transitive dependencies it pulls in (`go mod why <pkg>`).
2. **License & Compliance**: Verify OSI-compliant licensing (MIT, Apache 2.0, BSD).
3. **Maintenance & Stability**: Verify active maintenance, semver adherence, and absence of open critical vulnerabilities.
