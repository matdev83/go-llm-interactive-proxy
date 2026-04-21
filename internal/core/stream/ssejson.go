package stream

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

const maxPooledSSEBufferCap = 4 << 20 // drop oversized buffers instead of pooling them

var sseBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func putSSEBuffer(buf *bytes.Buffer) {
	if buf == nil {
		return
	}
	buf.Reset()
	if buf.Cap() > maxPooledSSEBufferCap {
		return
	}
	sseBufPool.Put(buf)
}

// FlushSSEEventJSON writes one SSE record: "event: name\ndata: <json>\n\n" using a pooled buffer.
func FlushSSEEventJSON(w io.Writer, fl http.Flusher, eventName string, payload any) error {
	buf := sseBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer putSSEBuffer(buf)
	buf.WriteString("event: ")
	buf.WriteString(eventName)
	buf.WriteString("\ndata: ")
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return err
	}
	buf.WriteByte('\n')
	if _, err := w.Write(buf.Bytes()); err != nil {
		return err
	}
	fl.Flush()
	return nil
}

// FlushSSEDataJSON writes one SSE data line: "data: <json>\n\n" using a pooled buffer.
func FlushSSEDataJSON(w io.Writer, fl http.Flusher, payload any) error {
	buf := sseBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer putSSEBuffer(buf)
	buf.WriteString("data: ")
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return err
	}
	buf.WriteByte('\n')
	if _, err := w.Write(buf.Bytes()); err != nil {
		return err
	}
	fl.Flush()
	return nil
}

// FlushSSEDataJoined writes data: prefix+mid+suffix followed by "\n\n" using a pooled buffer.
// prefix+mid+suffix must concatenate to one valid JSON value (no leading "data: " prefix).
func FlushSSEDataJoined(w io.Writer, fl http.Flusher, prefix, mid, suffix []byte) error {
	buf := sseBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer putSSEBuffer(buf)
	buf.WriteString("data: ")
	buf.Write(prefix)
	buf.Write(mid)
	buf.Write(suffix)
	buf.WriteString("\n\n")
	if _, err := w.Write(buf.Bytes()); err != nil {
		return err
	}
	fl.Flush()
	return nil
}
