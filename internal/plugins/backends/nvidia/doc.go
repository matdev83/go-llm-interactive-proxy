// Package nvidia implements the NVIDIA NIM backend connector supporting both
// Chat Completions (/v1/chat/completions) and Responses (/v1/responses) upstream
// flavors. It maps lipapi.Call to the selected flavor using openai-go v3,
// applying NVIDIA-specific payload mutations (max_tokens remap, stream_options strip,
// extra_body pass-through) via SDK request options.
package nvidia

// ID is the reserved plugin identifier for the NVIDIA NIM backend.
const ID = "nvidia"
