package api

import "context"

type userIDKey struct{}

// WithUserID returns a context carrying the authenticated user ID so
// downstream layers can propagate it across the request lifecycle.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// UserIDFromContext extracts the authenticated user ID previously stored via
// WithUserID. Returns "" when absent.
func UserIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(userIDKey{}).(string); ok {
		return id
	}
	return ""
}