# Fuzz seed corpus layout

This file is the **repo-level index**. Actual seed files live next to each package, e.g. `pkg/lipapi/testdata/fuzz/FuzzCallValidateJSON/…`.

Packed HTTP+selector blobs (leading `0x00` byte) for frontend decoders and binary hook seeds are generated with:

`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/init-fuzz-corpus.ps1`

Go native fuzzing loads the seed corpus from:

1. `f.Add(...)` inside each `func FuzzXxx(f *testing.F)`
2. **Committed files** under `{package_dir}/testdata/fuzz/FuzzXxx/`

`{package_dir}` is the directory of the Go package being tested (for example `pkg/lipapi` or `internal/core/routing`). The subdirectory name **`FuzzXxx` must match the fuzz function name exactly**.

## File → input mapping

| Fuzz argument type | File content |
|--------------------|--------------|
| `[]byte` | Raw bytes of the file |
| `string` | File decoded as UTF-8 text (whole file is one string) |

## Adding seeds

1. Pick the package and fuzz target (see `docs/release-gates.md` table and `Makefile` `test-fuzz`).
2. Create `testdata/fuzz/FuzzTargetName/your_seed` under that package (any filename; avoid secrets).
3. Run a short fuzz locally to ensure it loads:  
   `go test -fuzz=FuzzTargetName$ -fuzztime=2s -run=^$ ./path/to/pkg`

Interesting inputs from local fuzzing can be copied from the fuzz cache directory; prefer minimal reproducers.
