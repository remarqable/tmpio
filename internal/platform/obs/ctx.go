// Package obs carries request correlation through contexts.
package obs

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/remarqable/tmpio/internal/platform/logger"
)

type ctxKey struct{}

// Trace is the per-request correlation record.
type Trace struct {
	RequestID string
	TenantID  int64
	UserID    int64
	Route     string
}

// With attaches a trace to ctx.
func With(ctx context.Context, t Trace) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

// Get returns the trace attached to ctx, if any.
func Get(ctx context.Context) Trace {
	if t, ok := ctx.Value(ctxKey{}).(Trace); ok {
		return t
	}
	return Trace{}
}

// From returns a logger bound to the trace in ctx. It never includes
// secrets or content; tenant and user IDs are log fields, not metric labels.
func From(ctx context.Context) *zerolog.Logger {
	t := Get(ctx)
	l := logger.Get().With()
	if t.RequestID != "" {
		l = l.Str("request_id", t.RequestID)
	}
	if t.TenantID != 0 {
		l = l.Int64("tenant_id", t.TenantID)
	}
	if t.UserID != 0 {
		l = l.Int64("user_id", t.UserID)
	}
	lg := l.Logger()
	return &lg
}
