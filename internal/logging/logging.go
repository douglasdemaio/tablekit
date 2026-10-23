package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
)

type ctxKey int

const requestIDKey ctxKey = iota

func New(env string, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if env == "development" {
		opts.Level = slog.LevelDebug
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func From(ctx context.Context, base *slog.Logger) *slog.Logger {
	if id := RequestID(ctx); id != "" {
		return base.With("request_id", id)
	}
	return base
}
