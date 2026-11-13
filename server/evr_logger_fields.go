package server

import (
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// runtime.Logger is printf-style: Warn(format string, v ...interface{}). Passing
// it a zap.Field is not a type error, but Sprintf renders the field as
// "%!(EXTRA zapcore.Field=...)" appended to the message and the structured
// fields are lost. Use these helpers (or runtime.Logger.WithFields) whenever
// the call site has zap fields. TestNoZapFieldsOnRuntimeLogger fails on the
// bad shape.

// runtimeLoggerWithZapFields returns l carrying fields as structured fields.
func runtimeLoggerWithZapFields(l runtime.Logger, fields []zap.Field) runtime.Logger {
	if len(fields) == 0 {
		return l
	}
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range fields {
		f.AddTo(enc)
	}
	return l.WithFields(enc.Fields)
}

// The helpers call logSkip directly, with one extra frame of skip, so zap's
// caller and the "source" field name the code that called Log*, not this file.

// LogDebug logs msg verbatim (never as a format) at DEBUG with fields attached.
func LogDebug(l runtime.Logger, msg string, fields ...zap.Field) {
	l = runtimeLoggerWithZapFields(l, fields)
	if g, ok := l.(*RuntimeGoLogger); ok {
		g.logSkip(zapcore.DebugLevel, 1, msg)
		return
	}
	l.Debug("%s", msg)
}

// LogInfo logs msg verbatim at INFO with fields attached.
func LogInfo(l runtime.Logger, msg string, fields ...zap.Field) {
	l = runtimeLoggerWithZapFields(l, fields)
	if g, ok := l.(*RuntimeGoLogger); ok {
		g.logSkip(zapcore.InfoLevel, 1, msg)
		return
	}
	l.Info("%s", msg)
}

// LogWarn logs msg verbatim at WARN with fields attached.
func LogWarn(l runtime.Logger, msg string, fields ...zap.Field) {
	l = runtimeLoggerWithZapFields(l, fields)
	if g, ok := l.(*RuntimeGoLogger); ok {
		g.logSkip(zapcore.WarnLevel, 1, msg)
		return
	}
	l.Warn("%s", msg)
}

// LogError logs msg verbatim at ERROR with fields attached.
func LogError(l runtime.Logger, msg string, fields ...zap.Field) {
	l = runtimeLoggerWithZapFields(l, fields)
	if g, ok := l.(*RuntimeGoLogger); ok {
		g.logSkip(zapcore.ErrorLevel, 1, msg)
		return
	}
	l.Error("%s", msg)
}
