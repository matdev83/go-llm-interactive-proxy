package diag

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const stableTimestampBase = 1715620000

// StableCallToken returns a deterministic fingerprint for a canonical call.
// The token is stable across process runs for the same call content.
func StableCallToken(call *lipapi.Call) string {
	sum := stableCallSum(call)
	return hex.EncodeToString(sum[:8])
}

// StableCallID returns the caller-provided ID when present, otherwise a
// deterministic call-derived identifier suitable for trace and wire fallback use.
func StableCallID(call *lipapi.Call) string {
	if call != nil {
		if id := strings.TrimSpace(call.ID); id != "" {
			return id
		}
	}
	return "call_" + StableCallToken(call)
}

// StableUnix returns a deterministic Unix timestamp derived from the call.
// It is intentionally stable across runs so wire fallbacks stay reproducible.
func StableUnix(call *lipapi.Call) int64 {
	sum := stableCallSum(call)
	// Keep the value in a recent-looking range while staying deterministic.
	offset := int64(binary.BigEndian.Uint32(sum[:4]) % 86_400)
	return stableTimestampBase + offset
}

// StableTime returns a deterministic UTC timestamp derived from the call.
func StableTime(call *lipapi.Call) time.Time {
	return time.Unix(StableUnix(call), 0).UTC()
}

func stableCallSum(call *lipapi.Call) [32]byte {
	var zero [32]byte
	if call == nil {
		return zero
	}
	cp := *call
	cp.ID = ""
	b, err := json.Marshal(cp)
	if err != nil {
		return zero
	}
	return sha256.Sum256(b)
}
