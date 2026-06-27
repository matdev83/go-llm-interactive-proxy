// Package huggingface implements the Hugging Face Inference Providers backend connector
// for OpenAI-compatible chat completions via router.huggingface.co.
//
// A route selector query param `?provider=<slug>` selects the upstream inference
// provider by appending `:<slug>` to the model string (e.g.
// `openai/gpt-oss-120b:sambanova`). No `provider` JSON field is sent; HF routing
// is encoded in the model suffix. Models that already carry a suffix after their
// last segment (e.g. `openai/gpt-oss-120b:fastest`) are left unchanged.
package huggingface

const (
	ID             = "huggingface"
	DefaultBaseURL = "https://router.huggingface.co/v1"
)
