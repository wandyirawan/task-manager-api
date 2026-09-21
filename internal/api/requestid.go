package api

import (
	"context"
)

type requestIDKey struct{}

// WithRequestID returns a context carrying the given request ID so downstream
// layers (service/repo) can propagate it for cross-layer log correlation.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext extracts the request ID previously stored via
// WithRequestID. Returns "" when absent.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}