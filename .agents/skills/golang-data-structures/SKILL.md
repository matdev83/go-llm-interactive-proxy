---
name: golang-data-structures
description: "Choose and use Go data structures correctly: slices, maps, strings, arrays, containers, generics, pointers, and standard slices/maps helpers. Use when reasoning about aliasing, nil values, capacity, iteration, copying, or representation-sensitive performance."
---

# Go data structures

Choose the representation from the contract and access pattern first. Measure memory and CPU behavior on the target workload before relying on layout or capacity assumptions.

## Slices

A slice is a descriptor over an array: pointer, length, and capacity. Slicing shares the backing array; `append` may reuse it or allocate a new one. If a returned slice must not let callers mutate retained storage, use `slices.Clone`. Use a full slice expression (`s[:len(s):len(s)]`) to prevent a later append from overwriting the original backing array when that is the intended boundary.

Nil and empty slices differ for equality and JSON (`nil` commonly marshals as `null`, an allocated empty slice as `[]`). Preserve whichever meaning the API requires. `slices.Sort`, `SortFunc`, `BinarySearch`, `Clone`, `Compact`, `DeleteFunc`, `Grow`, and `Equal` are available in the standard library; check the module’s Go version before using newer helpers.

## Maps

Maps have reference-like behavior and are not safe for concurrent mutation. Nil maps can be read and ranged over but panic on assignment. Use a map for keyed lookup; use a typed map plus a mutex for ordinary shared state, `sync.Map` only when its documented workload fits.

Go’s current map implementation uses Swiss-table techniques (introduced in Go 1.24), but layout, bucket growth, and iteration order remain implementation details. Never write code or performance claims that depend on internal bucket fields. Map iteration order is deliberately unspecified; sort keys when output must be stable.

## Strings and bytes

Strings are immutable byte sequences, not guaranteed UTF-8. Use `[]byte` for mutable or I/O buffers and `[]rune` when indexing Unicode code points; use a Unicode-aware package when grapheme clusters or normalization matter. Conversions between strings and byte slices copy in ordinary Go code.

## Generics, arrays, and containers

Use generics when the operation is type-independent and a type parameter keeps the caller typed. Use an interface when behavior, not representation, is the abstraction. Arrays encode a fixed length; slices are the usual collection. `container/heap`, `container/list`, and `container/ring` are specialized—prefer slices and explicit indexes when they are clearer.

See [containers](references/containers.md), [generics](references/generics.md), [map representation](references/map-internals.md), [pointers](references/pointers.md), and [slice representation](references/slice-internals.md).
