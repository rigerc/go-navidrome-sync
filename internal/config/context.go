package config

import (
	"context"
)

type ctxKey struct{}

func WithContext(ctx context.Context, cfg *Config) context.Context {
	return context.WithValue(ctx, ctxKey{}, cfg)
}

func FromContext(ctx context.Context) *Config {
	c, _ := ctx.Value(ctxKey{}).(*Config)
	return c
}
