package runtimehost

import "context"

type requestBindingKey struct{}

// RequestBinding is the safe, immutable generation binding attached to a
// request context. It exposes metadata snapshots and controlled pin transfer
// only — never mutable Generation internals, config, credentials, Built, App,
// or ProcessServices handles (req 4.5-4.8).
type RequestBinding struct {
	lease  *Lease
	status Status // frozen at dispatch bind
}

func newRequestBinding(lease *Lease) *RequestBinding {
	b := &RequestBinding{lease: lease}
	if lease != nil && lease.gen != nil {
		b.status = lease.gen.Status()
	}
	return b
}

func withRequestBinding(ctx context.Context, b *RequestBinding) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestBindingKey{}, b)
}

// BindingFromContext returns the request generation binding when present.
func BindingFromContext(ctx context.Context) (*RequestBinding, bool) {
	if ctx == nil {
		return nil, false
	}
	b, ok := ctx.Value(requestBindingKey{}).(*RequestBinding)
	if !ok || b == nil || b.lease == nil {
		return nil, false
	}
	return b, true
}

// Meta returns a defensive copy of the generation metadata frozen at bind time.
func (b *RequestBinding) Meta() GenerationMeta {
	if b == nil {
		return GenerationMeta{}
	}
	return b.status.Meta
}

// Status returns a defensive lifecycle/metadata snapshot frozen at bind time.
func (b *RequestBinding) Status() Status {
	if b == nil {
		return Status{}
	}
	return b.status
}

// TransferPin converts the request lease retain into an async/SSE/provider pin.
// Exactly one successful transfer is allowed; subsequent calls fail. After a
// successful transfer, the dispatcher's deferred Release becomes a no-op.
func (b *RequestBinding) TransferPin(kind PinKind) (*Pin, bool) {
	if b == nil || b.lease == nil {
		return nil, false
	}
	return b.lease.TransferPin(kind)
}

// Meta returns safe generation metadata for this lease.
func (l *Lease) Meta() GenerationMeta {
	if l == nil || l.gen == nil {
		return GenerationMeta{}
	}
	return l.gen.Status().Meta
}

// Status returns a defensive lifecycle/metadata snapshot for this lease.
func (l *Lease) Status() Status {
	if l == nil || l.gen == nil {
		return Status{}
	}
	return l.gen.Status()
}
