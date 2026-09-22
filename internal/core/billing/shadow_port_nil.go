package billing

import "reflect"

// IsNilPort reports whether an explicit interface port holds no usable
// implementation. It rejects both untyped nil and typed-nil values (nil
// pointers, maps, slices, functions, channels, or interfaces boxed in a
// non-nil interface) before any shadow or billing composition trusts them.
// Non-nil-capable concrete values (structs, arrays, scalars) are accepted.
// The helper holds no global or registry state.
func IsNilPort(port any) bool {
	if port == nil {
		return true
	}
	value := reflect.ValueOf(port)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
