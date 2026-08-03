# Connector capability disposition

## Operator guidance

Connector capability metadata has two separate planes:

- **Transport capability** describes the connector link: cancellation and bidirectional streaming.
- **Authoritative model capability** describes what the selected model is known to accept: tools,
  vision, documents, video, structured output, and similar canonical semantics.

Transport support is not evidence that a model supports those semantics. Operators should therefore
not infer model features from an OpenAI-compatible endpoint, its protocol name, or a model-listing
response that contains only identifiers.

OpenRouter and vLLM currently advertise streaming only in their static, resolved, and listed model
capabilities. This is deliberate: OpenRouter routes to many underlying models, and vLLM's model
identity alone does not establish a feature contract. Both remain streaming-only until authoritative,
model-aware facts are available. The connectors still advertise their independent transport facts.

## Multimodal conversion

The shared OpenAI-compatible adapter does not convert canonical image, document/file, or video parts
to provider-specific wire forms. Unsupported parts fail explicitly before a request is sent; they are
never silently discarded. Operators must select a connector/model with authoritative multimodal
capabilities and a conversion-capable adapter before routing such requests.
