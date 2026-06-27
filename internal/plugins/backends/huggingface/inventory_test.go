package huggingface_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/huggingface"
)

func TestNew_openAICompatibleInventory(t *testing.T) {
	t.Parallel()
	handlerErrs := make(chan error, 1)
	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			err := fmt.Errorf("path = %q", r.URL.Path)
			select {
			case handlerErrs <- err:
			default:
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"meta-llama/Llama-3.1-8B-Instruct"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	be := huggingface.New(huggingface.Config{
		BaseURL:    modelsSrv.URL + "/v1",
		APIKey:     "hf-test",
		HTTPClient: modelsSrv.Client(),
	})
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-handlerErrs:
		t.Fatal(err)
	default:
	}
	if len(snap.Models) != 1 {
		t.Fatalf("models = %+v", snap.Models)
	}
	if snap.Models[0].NativeID != "meta-llama/Llama-3.1-8B-Instruct" {
		t.Fatalf("NativeID = %q", snap.Models[0].NativeID)
	}
	if snap.Models[0].CanonicalID != "meta-llama/Llama-3.1-8B-Instruct" {
		t.Fatalf("CanonicalID = %q", snap.Models[0].CanonicalID)
	}
}
