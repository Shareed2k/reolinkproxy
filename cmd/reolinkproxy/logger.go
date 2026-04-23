package main

import (
	"fmt"
	stdlog "log"
	"os"
	"strings"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type appLogger struct {
	mu    sync.RWMutex
	level zap.AtomicLevel
	base  *zap.Logger
	sugar *zap.SugaredLogger
}

var log = newAppLogger()

func newAppLogger() *appLogger {
	level := zap.NewAtomicLevelAt(zap.InfoLevel)
	base := buildZapLogger(level)
	return &appLogger{
		level: level,
		base:  base,
		sugar: base.Sugar(),
	}
}

func buildZapLogger(level zap.AtomicLevel) *zap.Logger {
	cfg := zap.NewProductionConfig()
	cfg.Level = level
	cfg.Encoding = "console"
	cfg.DisableStacktrace = true
	cfg.EncoderConfig.TimeKey = "time"
	cfg.EncoderConfig.LevelKey = "level"
	cfg.EncoderConfig.MessageKey = "msg"
	cfg.EncoderConfig.CallerKey = ""
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	cfg.EncoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder

	logger, err := cfg.Build(zap.AddCallerSkip(1))
	if err != nil {
		panic(err)
	}
	return logger
}

func (l *appLogger) Configure(rawLevel string) error {
	level, err := parseLogLevel(rawLevel)
	if err != nil {
		return err
	}
	l.level.SetLevel(level)
	stdlog.SetFlags(0)
	stdlog.SetOutput(os.Stderr)
	return nil
}

func parseLogLevel(raw string) (zapcore.Level, error) {
	if strings.TrimSpace(raw) == "" {
		return zap.InfoLevel, nil
	}

	var level zapcore.Level
	if err := level.Set(strings.ToLower(strings.TrimSpace(raw))); err != nil {
		return zap.InfoLevel, fmt.Errorf("invalid log level %q", raw)
	}
	return level, nil
}

func (l *appLogger) Sync() {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.base != nil {
		_ = l.base.Sync()
	}
}

func (l *appLogger) Debugf(format string, args ...any) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.sugar.Debugf(format, args...)
}

func (l *appLogger) Infof(format string, args ...any) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.sugar.Infof(format, args...)
}

func (l *appLogger) Warnf(format string, args ...any) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.sugar.Warnf(format, args...)
}

func (l *appLogger) Errorf(format string, args ...any) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.sugar.Errorf(format, args...)
}

func (l *appLogger) Fatalf(format string, args ...any) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	l.sugar.Fatalf(format, args...)
}

func (l *appLogger) Printf(format string, args ...any) {
	l.Infof(format, args...)
}

func (l *appLogger) Print(args ...any) {
	l.Infof("%s", fmt.Sprint(args...))
}

func (l *appLogger) Println(args ...any) {
	l.Infof("%s", fmt.Sprintln(args...))
}

func (l *appLogger) Fatal(args ...any) {
	l.Fatalf("%s", fmt.Sprint(args...))
}
