package logger

import (
	"context"
	"log/slog"

	"github.com/charmbracelet/log"
)

type charmHandler struct {
	logger *log.Logger
	attrs  []slog.Attr
}

func (h *charmHandler) Enabled(_ context.Context, level slog.Level) bool {
	switch level {
	case slog.LevelDebug:
		return h.logger.GetLevel() <= log.DebugLevel
	case slog.LevelInfo:
		return h.logger.GetLevel() <= log.InfoLevel
	case slog.LevelWarn:
		return h.logger.GetLevel() <= log.WarnLevel
	case slog.LevelError:
		return h.logger.GetLevel() <= log.ErrorLevel
	}
	return true
}

func (h *charmHandler) Handle(_ context.Context, r slog.Record) error {
	var args []interface{}
	r.Attrs(func(a slog.Attr) bool {
		args = append(args, a.Key, a.Value.Any())
		return true
	})

	switch r.Level {
	case slog.LevelDebug:
		h.logger.Debug(r.Message, args...)
	case slog.LevelInfo:
		h.logger.Info(r.Message, args...)
	case slog.LevelWarn:
		h.logger.Warn(r.Message, args...)
	case slog.LevelError:
		h.logger.Error(r.Message, args...)
	}
	return nil
}

func (h *charmHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newLogger := h.logger.With()
	for _, a := range attrs {
		newLogger = newLogger.With(a.Key, a.Value.Any())
	}
	return &charmHandler{logger: newLogger, attrs: append(h.attrs, attrs...)}
}

func (h *charmHandler) WithGroup(name string) slog.Handler {
	newLogger := h.logger.WithPrefix(name)
	return &charmHandler{logger: newLogger, attrs: h.attrs}
}
