package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/charmbracelet/log"

	"github.com/honeybyhomo/oak/internal/config"
)

// Logger wraps CharmLog with operation tracking
type Logger struct {
	*log.Logger
}

// Operation tracks a long-running operation with timing
type Operation struct {
	logger    *Logger
	name      string
	startTime time.Time
}

// New creates a new logger instance based on configuration
func New(cfg *config.LoggingConfig) *Logger {
	return NewWithWriter(cfg, os.Stdout)
}

// NewWithWriter creates a new logger with a custom writer (for testing)
func NewWithWriter(cfg *config.LoggingConfig, w io.Writer) *Logger {
	l := log.New(w)

	l.SetTimeFormat("15:04:05")
	l.SetReportTimestamp(true)

	switch cfg.Level {
	case "debug":
		l.SetLevel(log.DebugLevel)
	case "info":
		l.SetLevel(log.InfoLevel)
	case "warn":
		l.SetLevel(log.WarnLevel)
	case "error":
		l.SetLevel(log.ErrorLevel)
	default:
		l.SetLevel(log.InfoLevel)
	}

	if cfg.Format == "json" {
		l.SetFormatter(log.JSONFormatter)
	} else {
		l.SetFormatter(log.TextFormatter)
	}

	return &Logger{Logger: l}
}

// Track starts tracking an operation and returns an Operation object
func (l *Logger) Track(name string) *Operation {
	return &Operation{
		logger:    l,
		name:      name,
		startTime: time.Now(),
	}
}

// Complete logs the successful completion of an operation with record count and duration
func (op *Operation) Complete(records int) {
	duration := time.Since(op.startTime)
	msg := formatOperationMessage(op.name, records, duration)
	op.logger.Info(msg)
}

// Error logs a failed operation with error details
func (op *Operation) Error(err error) {
	if err == nil {
		return
	}
	duration := time.Since(op.startTime)
	msg := formatOperationMessage(op.name, 0, duration)
	op.logger.Error(msg, "error", err.Error())
}

func formatOperationMessage(name string, records int, duration time.Duration) string {
	durationStr := formatDuration(duration)
	return fmt.Sprintf("[%d rec, %s] %s", records, durationStr, name)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.3fs", d.Seconds())
	} else if d < time.Hour {
		minutes := int(d.Minutes())
		seconds := d.Seconds() - float64(minutes*60)
		return fmt.Sprintf("%dm%.3fs", minutes, seconds)
	} else {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) - hours*60
		seconds := d.Seconds() - float64(hours*3600) - float64(minutes*60)
		return fmt.Sprintf("%dh%dm%.3fs", hours, minutes, seconds)
	}
}

// SlogWarn returns a slog.Logger that logs at WARN level through CharmLog
func (l *Logger) SlogWarn() *slog.Logger {
	warnLogger := l.Logger.With()
	warnLogger.SetLevel(log.WarnLevel)
	return slog.New(&charmHandler{logger: warnLogger})
}
