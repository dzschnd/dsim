package httputil

import (
	"context"
	"time"
)

const requestTimeout time.Duration = 30 * time.Second

func WithRequestTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, requestTimeout)
}
