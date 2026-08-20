# Slice behavior

`len` is the visible element count and `cap` is the maximum length available before a new backing array may be allocated. `append` can mutate an array shared with another slice, so do not retain a view across an ownership boundary without cloning or clipping capacity.

```go
view := data[:n:n]       // later append cannot overwrite data beyond n
copyForCaller := slices.Clone(data[:n])
```

A nil slice and an allocated empty slice both have length zero but may differ in JSON, reflection, and API semantics. Preserve that distinction intentionally. Growth strategy and allocation sizes are runtime details; measure rather than relying on a fixed capacity multiplier.
