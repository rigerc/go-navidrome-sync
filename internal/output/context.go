package output

import "context"

type ctxKey struct{}

func WithContext(ctx context.Context, manager *Manager) context.Context {
	return context.WithValue(ctx, ctxKey{}, manager)
}

func FromContext(ctx context.Context) *Manager {
	manager, _ := ctx.Value(ctxKey{}).(*Manager)
	return manager
}
