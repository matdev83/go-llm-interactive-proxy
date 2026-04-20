package testkit

import (
	"net/http/httptest"
	"strings"
)

type SSEFrame struct {
	Event string
	Data  string
}

func ParseRecorderSSE(rec *httptest.ResponseRecorder) []SSEFrame {
	return ParseSSEBody(rec.Body.String())
}

func ParseSSEBody(body string) []SSEFrame {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var frames []SSEFrame
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var ev, data string
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimRight(line, "\r")
			switch {
			case strings.HasPrefix(line, "event:"):
				ev = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				d := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if data == "" {
					data = d
				} else {
					data += "\n" + d
				}
			}
		}
		frames = append(frames, SSEFrame{Event: ev, Data: data})
	}
	return frames
}
