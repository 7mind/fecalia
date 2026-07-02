package log

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Field key conventions. Per-path events carry FieldPath so logs can be filtered
// by uplink; every component-scoped logger carries FieldComponent.
const (
	FieldComponent = "component"
	FieldPath      = "path"
)

// Logger is the structured-logging surface the rest of wanbond depends on, so no
// package imports log/slog directly. It supports component- and path-scoped
// child loggers whose scope attributes are attached to every subsequent record.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	// Component returns a child logger tagged with the given component name.
	Component(name string) Logger
	// Path returns a child logger tagged with the given path name.
	Path(name string) Logger
}

// slogLogger is the slog-backed implementation of Logger.
type slogLogger struct {
	l *slog.Logger
}

// New builds a JSON-handler Logger writing to w at the given level. An unknown
// level is a configuration error (fail fast); an empty level defaults to info.
func New(level string, w io.Writer) (Logger, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	return &slogLogger{l: slog.New(h)}, nil
}

func parseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("log: unknown level %q (want debug|info|warn|error)", level)
	}
}

func (s *slogLogger) Debug(msg string, args ...any) { s.l.Debug(msg, args...) }
func (s *slogLogger) Info(msg string, args ...any)  { s.l.Info(msg, args...) }
func (s *slogLogger) Warn(msg string, args ...any)  { s.l.Warn(msg, args...) }
func (s *slogLogger) Error(msg string, args ...any) { s.l.Error(msg, args...) }

func (s *slogLogger) Component(name string) Logger {
	return &slogLogger{l: s.l.With(FieldComponent, name)}
}

func (s *slogLogger) Path(name string) Logger {
	return &slogLogger{l: s.l.With(FieldPath, name)}
}
