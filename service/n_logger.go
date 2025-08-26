package service

import (
	"fmt"
	"runtime"
	"strings"

	nkruntime "github.com/heroiclabs/nakama-common/runtime"

	"go.uber.org/zap"
)

var _ = nkruntime.Logger(&NEVRLogger{})

// NEVRLogger is a custom logger that implements the nkruntime.Logger interface
// It transparently supports both fmt.Sprintf style logging and zap.Field style logging
type NEVRLogger struct {
	logger *zap.Logger
	fields map[string]interface{}
}

func NewNEVRLogger(logger *zap.Logger) *NEVRLogger {
	return &NEVRLogger{
		fields: make(map[string]interface{}),
		logger: logger.WithOptions(zap.AddCallerSkip(1)).With(zap.String("runtime", "go")),
	}
}

func (l *NEVRLogger) Logger() *zap.Logger {
	return l.logger
}

func (l *NEVRLogger) getFileLine() zap.Field {
	_, filename, line, ok := runtime.Caller(2)
	if !ok {
		return zap.Skip()
	}
	filenameSplit := strings.SplitN(filename, "@", 2)
	if len(filenameSplit) >= 2 {
		filename = filenameSplit[1]
	}
	return zap.String("source", fmt.Sprintf("%v:%v", filename, line))
}

func isZapSyntax(v ...interface{}) bool {
	for _, value := range v {
		_, ok := value.(zap.Field)
		if ok {
			return true
		}
	}
	return false
}

func toZapFields(v ...any) []zap.Field {
	fields := make([]zap.Field, 0, len(v))
	for _, value := range v {
		if f, ok := value.(zap.Field); ok {
			fields = append(fields, f)
		} else {
			fields = append(fields, zap.Any("arg", value))
		}
	}
	return fields
}

func (l *NEVRLogger) Debug(format string, v ...interface{}) {
	if isZapSyntax(v...) {
		l.logger.Debug(format, toZapFields(v...)...)
	} else if l.logger.Core().Enabled(zap.DebugLevel) {
		msg := fmt.Sprintf(format, v...)
		l.logger.Debug(msg)
	}
}

func (l *NEVRLogger) Info(format string, v ...interface{}) {
	if l.logger.Core().Enabled(zap.InfoLevel) {
		msg := fmt.Sprintf(format, v...)
		l.logger.Info(msg)
	}
}

func (l *NEVRLogger) Warn(format string, v ...interface{}) {
	if l.logger.Core().Enabled(zap.WarnLevel) {
		msg := fmt.Sprintf(format, v...)
		l.logger.Warn(msg, l.getFileLine())
	}
}

func (l *NEVRLogger) Error(format string, v ...interface{}) {
	if l.logger.Core().Enabled(zap.ErrorLevel) {
		msg := fmt.Sprintf(format, v...)
		l.logger.Error(msg, l.getFileLine())
	}
}

func (l *NEVRLogger) WithField(key string, v interface{}) nkruntime.Logger {
	return l.WithFields(map[string]interface{}{key: v})
}

func (l *NEVRLogger) WithFields(fields map[string]interface{}) nkruntime.Logger {
	f := make([]zap.Field, 0, len(fields)+len(l.fields))
	newFields := make(map[string]interface{}, len(fields)+len(l.fields))
	for k, v := range l.fields {
		newFields[k] = v
	}
	for k, v := range fields {
		if k == "runtime" {
			continue
		}
		newFields[k] = v
		f = append(f, zap.Any(k, v))
	}

	return &NEVRLogger{
		logger: l.logger.With(f...),
		fields: newFields,
	}
}

func (l *NEVRLogger) Fields() map[string]interface{} {
	return l.fields
}
