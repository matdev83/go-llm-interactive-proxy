---
name: golang-data-modernize
description: Leverage modern Go language capabilities (Go 1.22–1.26), generics, range-over-func iterators, Swiss Tables maps, standard slices/maps helpers, struct memory alignment, and safe API modernization.
---

# Modern Go Data Structures & Language Capabilities

Go evolves steadily with backward compatibility while introducing significant language enhancements and performance improvements. This guide outlines idiomatic data structures, generics, iterators, and modernization practices.

---

## 1. Modern Go Language Capabilities (Go 1.22–1.26)

### Standard `slices` and `maps` Packages
Avoid hand-rolling common slice and map operations. Use the standard generic packages:

~~~go
import (
    "maps"
    "slices"
)

// Slices operations
if slices.Contains(allowedRoles, userRole) { ... }
slices.Sort(items)
deduped := slices.Compact(sortedItems)
clonedSlice := slices.Clone(originalSlice)

// Maps operations
clonedMap := maps.Clone(configMap)
maps.DeleteFunc(sessionMap, func(k string, v Session) bool {
    return v.IsExpired()
})
~~~

### Range-Over-Func Iterators (Go 1.23+)
Use standard `iter.Seq` and `iter.Seq2` to implement custom push-iterators without allocating intermediate slices:

~~~go
import "iter"

type Tree[T any] struct {
    value       T
    left, right *Tree[T]
}

// In-order traversal iterator
func (t *Tree[T]) All() iter.Seq[T] {
    return func(yield func(T) bool) {
        var walk func(node *Tree[T]) bool
        walk = func(node *Tree[T]) bool {
            if node == nil {
                return true
            }
            if !walk(node.left) { return false }
            if !yield(node.value) { return false }
            return walk(node.right)
        }
        walk(t)
    }
}

// Usage with native for-range:
for val := range tree.All() {
    fmt.Println(val)
}
~~~

---

## 2. Go Data Structures & Memory Layout

### Slice Memory Semantics & Sub-slice Leaking
- **Slice Header**: A slice is a lightweight 24-byte struct containing `[Pointer, Len, Cap]`.
- **Sub-slice Memory Leak**: Slicing a small substring from a large memory buffer keeps the entire backing array reachable by the GC:
~~~go
// DANGEROUS: Retains 10MB backing array in memory
func ReadID(r io.Reader) []byte {
    buf := make([]byte, 10*1024*1024)
    _, _ = r.Read(buf)
    return buf[:16] // Leaks entire 10MB!
}

// SAFE: Clone the slice to release the large backing array
func ReadIDSafe(r io.Reader) []byte {
    buf := make([]byte, 10*1024*1024)
    _, _ = r.Read(buf)
    return slices.Clone(buf[:16])
}
~~~

### Maps & Swiss Tables (Go 1.24+)
- Current Go implementations use **Swiss Tables** for hash map storage, delivering faster lookup speeds, lower memory overhead, and cache-friendly quadratic probing.
- Maps are **not concurrent-safe**: concurrent read/write or concurrent writes will crash the runtime. Use `sync.RWMutex` or `sync.Map` for concurrent access.

### Struct Field Alignment & Memory Padding
Arrange struct fields from largest to smallest to avoid wasteful byte padding on 64-bit architectures:

~~~go
// BAD: 32 bytes due to alignment padding
type Unoptimized struct {
    flag1 bool    // 1 byte + 7 bytes padding
    id    int64   // 8 bytes
    flag2 bool    // 1 byte + 7 bytes padding
    count int64   // 8 bytes
}

// GOOD: 24 bytes (flag1 and flag2 packed together)
type Optimized struct {
    id    int64   // 8 bytes
    count int64   // 8 bytes
    flag1 bool    // 1 byte
    flag2 bool    // 1 byte + 6 bytes padding at end
}
~~~

---

## 3. Generics & Type Constraints

Use generics when an algorithm or data structure is completely independent of the element type:

~~~go
// Generic Set implementation
type Set[T comparable] struct {
    items map[T]struct{}
}

func NewSet[T comparable](elems ...T) *Set[T] {
    s := &Set[T]{items: make(map[T]struct{}, len(elems))}
    for _, elem := range elems {
        s.items[elem] = struct{}{}
    }
    return s
}

func (s *Set[T]) Contains(elem T) bool {
    _, exists := s.items[elem]
    return exists
}
~~~

---

## 4. Modernization & Migration Recipes

1. **Deprecated `io/ioutil`**:
   - `ioutil.ReadFile` $\rightarrow$ `os.ReadFile`
   - `ioutil.WriteFile` $\rightarrow$ `os.WriteFile`
   - `ioutil.ReadAll` $\rightarrow$ `io.ReadAll`
   - `ioutil.Discard` $\rightarrow$ `io.Discard`
2. **Safe String/Slice Zero-Copy Conversions**:
   - In Go >= 1.20, use `unsafe.StringData`, `unsafe.SliceData`, `unsafe.String`, and `unsafe.Slice` instead of complex reflect header manipulation when zero-copy conversions are justified and safe.
