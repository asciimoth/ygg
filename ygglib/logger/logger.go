package logger

import (
	"fmt"
	"io"
	stdlog "log"
	"os"
	"strings"
	"sync/atomic"
)

// Logger provides logging for a Device.
// Implementations must be safe for concurrent use.
type Logger interface {
	Debug(args ...any)
	Debugf(format string, args ...any)
	Info(args ...any)
	Infof(format string, args ...any)
	Warn(args ...any)
	Warnf(format string, args ...any)
	Err(args ...any)
	Errf(format string, args ...any)
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

type Level uint32

const (
	LevelError Level = iota
	LevelWarn
	LevelInfo
	LevelDebug
)

func ParseLevel(level string) (Level, bool) {
	switch strings.ToLower(level) {
	case "error", "err":
		return LevelError, true
	case "warn", "warning":
		return LevelWarn, true
	case "info":
		return LevelInfo, true
	case "debug", "trace":
		return LevelDebug, true
	default:
		return LevelInfo, false
	}
}

// StdLogger implements Logger using Go's standard log package.
type StdLogger struct {
	log   *stdlog.Logger
	level atomic.Uint32
}

func New(w io.Writer, prefix string, flag int) *StdLogger {
	l := &StdLogger{
		log: stdlog.New(w, prefix, flag),
	}
	l.SetLevel(LevelInfo)
	return l
}

func Discard() *StdLogger {
	return New(io.Discard, "", 0)
}

func (l *StdLogger) SetLevel(level Level) {
	l.level.Store(uint32(level))
}

func (l *StdLogger) Debug(args ...any) {
	l.output(LevelDebug, "DEBUG ", sprint(args...))
}

func (l *StdLogger) Debugf(format string, args ...any) {
	l.output(LevelDebug, "DEBUG ", fmt.Sprintf(format, args...))
}

func (l *StdLogger) Info(args ...any) {
	l.output(LevelInfo, "INFO ", sprint(args...))
}

func (l *StdLogger) Infof(format string, args ...any) {
	l.output(LevelInfo, "INFO ", fmt.Sprintf(format, args...))
}

func (l *StdLogger) Warn(args ...any) {
	l.output(LevelWarn, "WARN ", sprint(args...))
}

func (l *StdLogger) Warnf(format string, args ...any) {
	l.output(LevelWarn, "WARN ", fmt.Sprintf(format, args...))
}

func (l *StdLogger) Err(args ...any) {
	l.output(LevelError, "ERROR ", sprint(args...))
}

func (l *StdLogger) Errf(format string, args ...any) {
	l.output(LevelError, "ERROR ", fmt.Sprintf(format, args...))
}

func (l *StdLogger) Fatal(args ...any) {
	l.output(LevelError, "FATAL ", sprint(args...))
	os.Exit(1)
}

func (l *StdLogger) Fatalf(format string, args ...any) {
	l.output(LevelError, "FATAL ", fmt.Sprintf(format, args...))
	os.Exit(1)
}

func (l *StdLogger) output(level Level, prefix, msg string) {
	if l == nil || l.log == nil || uint32(level) > l.level.Load() {
		return
	}
	_ = l.log.Output(3, prefix+msg)
}

func sprint(args ...any) string {
	return strings.TrimSuffix(fmt.Sprintln(args...), "\n")
}
