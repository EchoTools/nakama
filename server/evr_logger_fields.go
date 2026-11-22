package server

import (
	"fmt"
	goruntime "runtime"
	"strings"

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

// The helpers call logFromHelper directly, which skips one extra frame, so zap's
// caller and the "source" field name the code that called Log*, not this file.
//
// This lives here, and not on RuntimeGoLogger, on purpose: runtime_go_logger.go
// is upstream Nakama code and is kept identical to upstream.

// logFromHelper logs msg verbatim at level with fields attached through g, for
// the Log* helpers below, which sit one frame above the call site that should
// be reported. It reads g.logger directly (same package) and adds one frame of
// caller skip on top of the one NewRuntimeGoLogger already applies.
//
// A caller-supplied "runtime" key is dropped, as RuntimeGoLogger.WithFields
// does, so nothing can override or duplicate the base runtime=go field. Warn and
// above carry a "source" field, as RuntimeGoLogger.Warn/Error do.
func logFromHelper(g *RuntimeGoLogger, level zapcore.Level, msg string, fields []zap.Field) {
	if !g.logger.Core().Enabled(level) {
		return
	}
	drop := false
	for _, f := range fields {
		if f.Key == "runtime" {
			drop = true
			break
		}
	}
	if drop || level >= zapcore.WarnLevel {
		all := make([]zap.Field, 0, len(fields)+1)
		for _, f := range fields {
			if f.Key != "runtime" {
				all = append(all, f)
			}
		}
		if level >= zapcore.WarnLevel {
			all = append(all, helperCallSource())
		}
		fields = all
	}
	zl := g.logger.WithOptions(zap.AddCallerSkip(1))
	switch level {
	case zapcore.DebugLevel:
		zl.Debug(msg, fields...)
	case zapcore.InfoLevel:
		zl.Info(msg, fields...)
	case zapcore.WarnLevel:
		zl.Warn(msg, fields...)
	default:
		zl.Error(msg, fields...)
	}
}

// helperCallSource is the "source" field for a Log* helper: the file:line that
// called Log*. Stack from here: helperCallSource, logFromHelper, Log*, call site.
func helperCallSource() zap.Field {
	_, filename, line, ok := goruntime.Caller(3)
	if !ok {
		return zap.Skip()
	}
	if parts := strings.SplitN(filename, "@", 2); len(parts) >= 2 {
		filename = parts[1]
	}
	return zap.String("source", fmt.Sprintf("%v:%v", filename, line))
}

// LogDebug logs msg verbatim (never as a format) at DEBUG with fields attached.
func LogDebug(l runtime.Logger, msg string, fields ...zap.Field) {
	if g, ok := l.(*RuntimeGoLogger); ok {
		logFromHelper(g, zapcore.DebugLevel, msg, fields)
		return
	}
	runtimeLoggerWithZapFields(l, fields).Debug("%s", msg)
}

// LogInfo logs msg verbatim at INFO with fields attached.
func LogInfo(l runtime.Logger, msg string, fields ...zap.Field) {
	if g, ok := l.(*RuntimeGoLogger); ok {
		logFromHelper(g, zapcore.InfoLevel, msg, fields)
		return
	}
	runtimeLoggerWithZapFields(l, fields).Info("%s", msg)
}

// LogWarn logs msg verbatim at WARN with fields attached.
func LogWarn(l runtime.Logger, msg string, fields ...zap.Field) {
	if g, ok := l.(*RuntimeGoLogger); ok {
		logFromHelper(g, zapcore.WarnLevel, msg, fields)
		return
	}
	runtimeLoggerWithZapFields(l, fields).Warn("%s", msg)
}

// LogError logs msg verbatim at ERROR with fields attached.
func LogError(l runtime.Logger, msg string, fields ...zap.Field) {
	if g, ok := l.(*RuntimeGoLogger); ok {
		logFromHelper(g, zapcore.ErrorLevel, msg, fields)
		return
	}
	runtimeLoggerWithZapFields(l, fields).Error("%s", msg)
}
