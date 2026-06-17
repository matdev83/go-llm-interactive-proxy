package holdalive_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/holdalive"
)

type recordingWriter struct {
	statuses []int
	flushed  int
}

func (w *recordingWriter) Header() http.Header { return http.Header{} }
func (w *recordingWriter) Write(b []byte) (int, error) {
	return len(b), nil
}

func (w *recordingWriter) WriteHeader(statusCode int) {
	w.statuses = append(w.statuses, statusCode)
}

func (w *recordingWriter) Flush() {
	w.flushed++
}

func TestWait_sendsInformationalKeepaliveWithoutChangingResult(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	got, err := holdalive.Wait(context.Background(), w, holdalive.Config{
		Enabled:  true,
		Interval: time.Millisecond,
	}, func(context.Context) (string, error) {
		time.Sleep(5 * time.Millisecond)
		return "done", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "done" {
		t.Fatalf("result: %q", got)
	}
	if len(w.statuses) == 0 || w.statuses[0] != http.StatusProcessing {
		t.Fatalf("statuses: %#v", w.statuses)
	}
	if w.flushed == 0 {
		t.Fatal("expected flush after informational status")
	}
}

func TestWait_returnsExecuteErrorAfterInformationalKeepalive(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	boom := errors.New("boom")
	_, err := holdalive.Wait(context.Background(), w, holdalive.Config{
		Enabled:  true,
		Interval: time.Millisecond,
	}, func(context.Context) (string, error) {
		time.Sleep(5 * time.Millisecond)
		return "", boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	if len(w.statuses) == 0 || w.statuses[0] != http.StatusProcessing {
		t.Fatalf("statuses: %#v", w.statuses)
	}
}

func TestWait_contextCancelWaitsForExecuteToFinish(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		_, err := holdalive.Wait(ctx, w, holdalive.Config{
			Enabled:  true,
			Interval: time.Hour,
		}, func(context.Context) (string, error) {
			<-release
			return "done", nil
		})
		returned <- err
	}()

	cancel()
	select {
	case err := <-returned:
		t.Fatalf("Wait returned before Execute finished: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after Execute finished")
	}
}

func TestWait_disabledDoesNotWriteInformationalStatus(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	_, err := holdalive.Wait(context.Background(), w, holdalive.Config{},
		func(context.Context) (string, error) { return "done", nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(w.statuses) != 0 || w.flushed != 0 {
		t.Fatalf("statuses=%#v flushed=%d", w.statuses, w.flushed)
	}
}
