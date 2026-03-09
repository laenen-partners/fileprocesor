package fileprocesor

import "context"

type contextKey int

const callerKey contextKey = iota

// Caller represents the identity of the request originator.
type Caller struct {
	ServiceID string
	UserID    string
}

// CallerFromContext returns the Caller stored in ctx, or a zero Caller if none.
func CallerFromContext(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey).(Caller)
	return c
}

func withCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey, c)
}
