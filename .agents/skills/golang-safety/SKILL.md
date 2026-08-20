---
name: golang-safety
description: "Review Go for nil/interface traps, integer and float conversions, slices/maps aliasing, concurrent access, resource ownership, initialization, unsafe usage, and error loss. Use when preventing panics, races, data corruption, or security-relevant boundary mistakes."
---

# Go safety

Make invalid states explicit at boundaries and preserve ownership through the call graph. Safety rules depend on the type, operation, and contract; avoid absolute claims that erase useful distinctions.

## Nil and interfaces

Nil slices can be ranged over and appended to; nil maps can be read/ranged but not assigned. Check whether nil and empty have different wire or semantic meanings before normalizing them. An interface is nil only when both its dynamic type and value are absent; an interface holding a typed nil pointer is non-nil. Validate the dynamic value at the boundary or make the API’s nil policy explicit.

## Numbers

Check ranges before narrowing conversions, especially for lengths, indexes, timestamps, and untrusted values. Constants are evaluated exactly until converted: `const ok = 0.1 + 0.2 == 0.3` is true because these are untyped exact constants, while `float64(0.1)+float64(0.2) == float64(0.3)` is false on ordinary IEEE-754 implementations. Use integer arithmetic for money and define overflow/rounding behavior.

Do not compare floats for exact equality unless the values are controlled exact values; use a domain-appropriate tolerance or representation. Reject negative values before converting to unsigned types.

## Slices, maps, and concurrency

Slices may alias arrays; clone before retaining or returning mutable data across an ownership boundary. Map reads and iteration can run concurrently only when no goroutine mutates the map; concurrent read/write or write/write requires a mutex, atomic design, or another synchronized map. The race detector is a useful dynamic check, not a proof of all executions.

## Initialization and cleanup

Check every constructor/open error. `sync.Once` caches completion, not success; if initialization can fail, use `sync.OnceValues` or store and return the error:

```go
var openDB = sync.OnceValues(func() (*sql.DB, error) {
    return sql.Open("driver", dsn)
})

func DB() (*sql.DB, error) { return openDB() }
```

Still `PingContext` when readiness, credentials, or network reachability must be proved. Close resources on all paths, and decide whether a cleanup error should replace or join the primary error.

## Unsafe and boundaries

Use `unsafe` only when the documented representation and lifetime are required and a safe alternative is inadequate. A `uintptr` is an integer, not a retained pointer; converting through it can allow the object to become unreachable or violate pointer rules. Keep conversions within the documented expression patterns and do not assume the garbage collector is stationary. `unsafe.SliceData` was added in Go 1.20; use it only with a valid slice and carefully bounded length.

At file, network, SQL, template, and serialization boundaries validate sizes, paths, encodings, and credentials. Preserve errors and avoid logging secrets. See [nil safety](references/nil-safety.md) and [slice/map safety](references/slice-map-safety.md).
