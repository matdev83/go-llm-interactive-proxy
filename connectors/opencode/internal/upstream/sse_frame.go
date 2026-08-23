package upstream

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// SSE frames are decoded line-by-line under a per-frame payload cap so a
// single provider frame cannot force unbounded materialization or huge
// per-frame allocation. The raw line scanner reserves only enough headroom for
// the frame prefix; the payload cap itself is owned by decodeSSEFrame.
const (
	// maxSSEFrameBytes bounds the decoded payload of one SSE data: frame.
	maxSSEFrameBytes = 1 << 20 // 1 MiB

	// maxSSEFrameLineBytes bounds one raw scanner token: the data: prefix
	// plus a payload at the frame cap.
	maxSSEFrameLineBytes = maxSSEFrameBytes + len(sseFrameDataPrefix)
)

const sseFrameDataPrefix = "data:"

// errSSEFrameTooLarge reports a single SSE data: frame over the frame cap.
var errSSEFrameTooLarge = errors.New("opencode: SSE frame too large")

// decodeSSEFrame materializes one data frame only after its payload passes the
// frame cap. The per-line scanner enforces the same bound first; this check
// keeps the frame contract owned by the decode boundary itself.
func decodeSSEFrame(data []byte, dest any) error {
	if int64(len(data)) > maxSSEFrameBytes {
		return errSSEFrameTooLarge
	}
	return json.Unmarshal(data, dest)
}

// nextSSEDataFrame returns the payload of the next non-empty data: line,
// skipping event and other non-data lines under the same trimming rules.
// Scanner token overrun maps to errSSEFrameTooLarge here, so every SSE stream
// enforces the frame bound through one code path; io.EOF is returned at the
// end of the stream.
func nextSSEDataFrame(sc *bufio.Scanner) (string, error) {
	for {
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				if errors.Is(err, bufio.ErrTooLong) {
					return "", errSSEFrameTooLarge
				}
				return "", err
			}
			return "", io.EOF
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, sseFrameDataPrefix) {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, sseFrameDataPrefix)), nil
	}
}
