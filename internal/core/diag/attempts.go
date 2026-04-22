package diag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// AttemptLoader loads B2BUA attempt rows for diagnostics (typically b2bua.Store).
type AttemptLoader interface {
	LoadAttempts(ctx context.Context, aLegID string) ([]lipapi.AttemptRecord, error)
}

var _ AttemptLoader = (*b2bua.MemoryStore)(nil)

func attemptLoaderIsNil(store AttemptLoader) bool {
	if store == nil {
		return true
	}
	v := reflect.ValueOf(store)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// AttemptsHandler returns an http.Handler that GETs ?a_leg_id= and returns JSON attempt rows.
func AttemptsHandler(store AttemptLoader) (http.Handler, error) {
	if attemptLoaderIsNil(store) {
		return nil, errors.New("diag: AttemptsHandler: nil store")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		aLeg := r.URL.Query().Get("a_leg_id")
		if aLeg == "" {
			http.Error(w, "missing a_leg_id", http.StatusBadRequest)
			return
		}
		rows, err := store.LoadAttempts(r.Context(), aLeg)
		if err != nil {
			if errors.Is(err, b2bua.ErrALegNotFound) {
				http.Error(w, "a-leg not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(true)
		if err := enc.Encode(rows); err != nil {
			return
		}
	}), nil
}
