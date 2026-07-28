package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// PumpSSE wraps es with recovery keepalive, pumps canonical events through handle until
// it returns done or an error, and closes es on exit (joining close errors).
func PumpSSE(ctx context.Context, w http.ResponseWriter, es lipapi.EventStream, eofWithoutDone error, handle func(lipapi.Event) (done bool, err error)) (err error) {
	ka, err := WrapRecoveryKeepalive(es)
	if err != nil {
		return err
	}
	es = ka
	defer func() {
		if cerr := es.Close(); cerr != nil {
			closeErr := fmt.Errorf("stream: close event stream: %w", cerr)
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()

	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("stream: ResponseWriter is not a Flusher")
	}

	for {
		var ev lipapi.Event
		ev, err = es.Recv(ctx)
		if errors.Is(err, io.EOF) {
			if eofWithoutDone != nil {
				return eofWithoutDone
			}
			return fmt.Errorf("stream: ended without terminal event")
		}
		if err != nil {
			return err
		}
		if ev.Kind == lipapi.EventWarning && ev.WarningCode == KeepaliveEventCode {
			if _, err = io.WriteString(w, ": keepalive\n\n"); err != nil {
				return err
			}
			fl.Flush()
			continue
		}
		if ev.Kind == lipapi.EventError {
			return lipapi.NewStreamError(ev.ErrorCode, ev.ErrorMessage)
		}
		var done bool
		done, err = handle(ev)
		if done || err != nil {
			return err
		}
	}
}
