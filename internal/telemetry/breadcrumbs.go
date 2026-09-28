package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/getsentry/sentry-go"
)

// breadcrumbKeys hold tool payloads or credentials and never ride on a crash.
var breadcrumbKeys = map[string]bool{
	"arguments": true, "content": true, "text": true, "body": true, "prompt": true,
	"authorization": true, "token": true, "cookie": true,
}

// WithCrashBreadcrumbs tees next so each info-or-higher record also becomes a
// breadcrumb on the next crash event. It never raises an event itself.
func WithCrashBreadcrumbs(next slog.Handler) slog.Handler {
	return teeHandler{handlers: []slog.Handler{next, breadcrumbHandler{}}}
}

type teeHandler struct {
	handlers []slog.Handler
}

func (h teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h teeHandler) Handle(ctx context.Context, record slog.Record) error {
	var errs []error
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, record.Level) {
			errs = append(errs, handler.Handle(ctx, record.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (h teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		next[i] = handler.WithAttrs(attrs)
	}
	return teeHandler{handlers: next}
}

func (h teeHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		next[i] = handler.WithGroup(name)
	}
	return teeHandler{handlers: next}
}

type breadcrumbHandler struct {
	attrs []slog.Attr
}

func (h breadcrumbHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo && crashReportingActive()
}

func (h breadcrumbHandler) Handle(_ context.Context, record slog.Record) error {
	data := make(map[string]any, len(h.attrs)+record.NumAttrs())
	put := func(attr slog.Attr) bool {
		if breadcrumbKeys[strings.ToLower(attr.Key)] {
			data[attr.Key] = "[Filtered]"
		} else {
			data[attr.Key] = attr.Value.Resolve().Any()
		}
		return true
	}
	for _, attr := range h.attrs {
		put(attr)
	}
	record.Attrs(put)
	level := sentry.LevelInfo
	switch {
	case record.Level >= slog.LevelError:
		level = sentry.LevelError
	case record.Level >= slog.LevelWarn:
		level = sentry.LevelWarning
	}
	sentry.AddBreadcrumb(&sentry.Breadcrumb{
		Category:  "log",
		Message:   record.Message,
		Level:     level,
		Data:      data,
		Timestamp: record.Time,
	})
	return nil
}

func (h breadcrumbHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return breadcrumbHandler{attrs: append(append([]slog.Attr(nil), h.attrs...), attrs...)}
}

// WithGroup keeps breadcrumbs flat: their data is one level deep.
func (h breadcrumbHandler) WithGroup(string) slog.Handler { return h }
