// Package auth defines core-owned consuming ports for authentication and auth/session event
// delivery. Implementations of [Authenticator] and [RemoteDecider] map policy into
// [github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth] types; [EventSink] receives
// non-secret event DTOs. [EventDispatcher] applies [EventFailurePolicy] when delivering those
// events to a sink. Transport (HTTP) and remote clients live outside this package.
package auth
