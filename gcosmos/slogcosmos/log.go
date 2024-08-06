// Package slogcosmos provides a [cosmossdk.io/core/log.Logger] backed by log/slog.
package slogcosmos

import (
	"log/slog"

	clog "cosmossdk.io/log"
)

var _ clog.Logger = Logger{}

type Logger struct {
	log *slog.Logger
}

func NewLogger(log *slog.Logger) Logger {
	return Logger{log: log}
}

func (l Logger) Info(msg string, keyVals ...any) {
	l.log.Info(msg, keyVals...)
}

func (l Logger) Warn(msg string, keyVals ...any) {
	l.log.Warn(msg, keyVals...)
}

func (l Logger) Error(msg string, keyVals ...any) {
	l.log.Error(msg, keyVals...)
}

func (l Logger) Debug(msg string, keyVals ...any) {
	l.log.Debug(msg, keyVals...)
}

func (l Logger) With(keyVals ...any) clog.Logger {
	return Logger{log: l.log.With(keyVals...)}
}

func (l Logger) Impl() any {
	return l.log
}
