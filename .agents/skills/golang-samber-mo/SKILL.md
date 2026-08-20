---
name: golang-samber-mo
description: Use samber/mo for explicit generic Option, Result, Either, Task, and state values while preserving clear error and absence semantics.
---

# samber/mo

This guidance targets github.com/samber/mo v1.17. It provides typed value wrappers; it does not make Go code compiler-enforced exhaustive or nil-proof. A caller can ignore a wrapper, call a Must method, or unwrap an absent value and panic.

## Option

Some(v) represents a value and None[T]() represents absence. Use IsSome/IsNone plus Get, OrElse, or a deliberate Match/ForEach path. Keep an Option around when absence is meaningful; do not use it to hide an error that needs an error return.

JSON behavior is ordinary marshaling behavior for the type. None is not automatically omitted from a containing struct: without a pointer or omitempty-compatible representation chosen by the containing type, it may marshal as null. Test the exact wire shape. Do not claim that Option gives compile-time nil safety.

## Result and Either

Result[T] represents success or error. Use Ok/Err constructors, IsOk/IsError, Get, Error, OrElse, and Match according to the module API. Either[L,R] represents a left or right value; Left/Right plus Left()/Right() return a value and boolean, while MustLeft/MustRight panic when the side is wrong.

~~~go
result := mo.TupleToResult(load())
if result.IsError() {
    return result.Error()
}
value, err := result.Get()
if err != nil {
    return err
}
use(value)
~~~

Keep errors as errors when crossing ordinary Go APIs. Use ToEither when two domain branches are clearer than an error channel, and document which side is failure. Do not silently turn an error into a zero value.

## Transform and sequencing

Map transforms a present/success value. FlatMap chains a function that already returns Option or Result. Match/ForEach makes side effects explicit. The package's do-notation helpers may use MustGet internally and rely on the helper to short-circuit; inspect current examples before adopting them.

The type parameters improve accidental mixing of values, but they do not force handling. Prefer normal if err != nil code when a wrapper makes the control flow harder to read. Avoid nested Option/Result unless each layer conveys a different contract.

## Serialization and review

Verify MarshalJSON/UnmarshalJSON behavior in the installed version rather than inferring it from a type name. Decide whether absent, null, empty, and error states are distinct on the wire. Review panicking accessors, ignored results, error identity, copying/aliasing, and whether the wrapper leaks into a stable public API unnecessarily.
