package server

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestNoZapFieldsOnRuntimeLogger fails on any production call of the form
// runtimeLogger.Warn("msg", zap.Error(err)). runtime.Logger is printf-style, so
// the field is rendered as "%!(EXTRA ...)" in the message and the structured
// field is lost (the line logged at Failed-to-allocate-game-server did this).
// Neither go vet nor golangci-lint sees it: the interface method is not a
// recognised printf wrapper. This type-checks the package, so it is exact.
func TestNoZapFieldsOnRuntimeLogger(t *testing.T) {
	// CI does not pass -short (it runs `just test-audit`); CI=true also forces the guard on.
	if testing.Short() && os.Getenv("CI") == "" {
		t.Skip("type-checks the whole server package from source (~30s)")
	}
	fs := token.NewFileSet()
	pkg, err := build.Default.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, n := range pkg.GoFiles {
		f, err := parser.ParseFile(fs, filepath.Join(".", n), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	var typeErrs []error
	conf := types.Config{Importer: importer.ForCompiler(fs, "source", nil), Error: func(e error) { typeErrs = append(typeErrs, e) }}
	_, _ = conf.Check("server", fs, files, info) // errors are collected by conf.Error
	if len(typeErrs) > 0 {
		// A partial type-check would silently find nothing; refuse to pass.
		t.Fatalf("type-check failed, detector cannot be trusted: %v", typeErrs[0])
	}
	checked := 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			s, ok := c.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			sel := info.Selections[s]
			if sel == nil || !strings.HasSuffix(sel.Recv().String(), "nakama-common/runtime.Logger") {
				return true
			}
			checked++
			for _, a := range c.Args {
				if strings.HasSuffix(info.Types[a].Type.String(), "zap.Field") {
					t.Errorf("%s: zap.Field passed to printf-style runtime.Logger.%s; use Log%s(logger, msg, fields...)", fs.Position(c.Pos()), s.Sel.Name, s.Sel.Name)
					break
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("detector matched no runtime.Logger calls; it is not looking at the right type")
	}
}

type fieldTestRuntimeLogger struct {
	runtime.Logger
	z *zap.Logger
}

func (l fieldTestRuntimeLogger) Warn(format string, v ...interface{}) {
	l.z.Warn(fmt.Sprintf(format, v...))
}
func (l fieldTestRuntimeLogger) WithFields(m map[string]interface{}) runtime.Logger {
	var f []zap.Field
	for k, v := range m {
		f = append(f, zap.Any(k, v))
	}
	return fieldTestRuntimeLogger{z: l.z.With(f...)}
}

// LogWarn keeps the message clean and the fields structured, at the real level.
func TestLogWarnKeepsFieldsStructured(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	LogWarn(fieldTestRuntimeLogger{z: zap.New(core)}, "Failed to allocate game server", zap.String("region", "us-east"))
	got := logs.All()
	if len(got) != 1 || got[0].Level != zapcore.WarnLevel || got[0].Message != "Failed to allocate game server" {
		t.Fatalf("unexpected entries: %+v", got)
	}
	if got[0].ContextMap()["region"] != "us-east" {
		t.Fatalf("field lost: %+v", got[0].ContextMap())
	}
}

// zap's own caller must name the line that called Log*, at all four levels, not
// the helper that forwards to the logger. The "source" field (runtime.Caller)
// is carried only by Warn and Error, as RuntimeGoLogger.Warn/Error do.
func TestLogHelpersReportTheCallSite(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	rl := NewRuntimeGoLogger(zap.New(core, zap.AddCaller()))

	_, thisFile, line, _ := goruntime.Caller(0)
	LogDebug(rl, "debug-msg", zap.String("k", "v")) // line+1
	LogInfo(rl, "info-msg", zap.String("k", "v"))   // line+2
	LogWarn(rl, "warn-msg", zap.String("k", "v"))   // line+3
	LogError(rl, "error-msg", zap.String("k", "v")) // line+4

	levels := []zapcore.Level{zapcore.DebugLevel, zapcore.InfoLevel, zapcore.WarnLevel, zapcore.ErrorLevel}
	for i, level := range levels {
		entries := logs.FilterLevelExact(level).All()
		require.Len(t, entries, 1, "%s entries", level)
		want := fmt.Sprintf("%s:%d", thisFile, line+1+i)
		require.Equal(t, want, fmt.Sprintf("%s:%d", entries[0].Caller.File, entries[0].Caller.Line), "%s zap caller", level)
		source, hasSource := entries[0].ContextMap()["source"]
		if level >= zapcore.WarnLevel {
			require.True(t, hasSource, "%s must carry source", level)
			require.Equal(t, want, source, "%s source field", level)
		} else {
			require.False(t, hasSource, "%s must not carry source", level)
		}
		require.Equal(t, "v", entries[0].ContextMap()["k"])
	}
}

// RuntimeGoLogger.WithFields drops a caller-supplied "runtime" key so nothing
// can override or duplicate the base runtime=go field. The Log* helpers hand
// fields straight to zap, so they must drop it too.
func TestLogHelpersDropRuntimeFieldKey(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	rl := NewRuntimeGoLogger(zap.New(core))

	rl.WithFields(map[string]interface{}{"runtime": "lua", "k": "v"}).Info("via-with-fields")
	LogInfo(rl, "via-helper", zap.String("runtime", "lua"), zap.String("k", "v"))
	LogError(rl, "via-helper-error", zap.String("runtime", "lua"), zap.String("k", "v"))

	require.Equal(t, 3, logs.Len())
	for _, e := range logs.All() {
		n := 0
		for _, f := range e.Context {
			if f.Key == "runtime" {
				n++
			}
		}
		require.Equal(t, 1, n, "%s: exactly one runtime key", e.Message)
		require.Equal(t, "go", e.ContextMap()["runtime"], "%s: runtime stays go", e.Message)
		require.Equal(t, "v", e.ContextMap()["k"], "%s: other fields kept", e.Message)
	}
}

// BenchmarkLogWarn measures the per-call cost of the Log* helpers on an enabled
// level, the case that matters on a hot path.
func BenchmarkLogWarn(b *testing.B) {
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(io.Discard), zapcore.DebugLevel)
	rl := NewRuntimeGoLogger(zap.New(core, zap.AddCaller()))
	err := errors.New("boom")
	b.ReportAllocs()
	for b.Loop() {
		LogWarn(rl, "Failed to allocate game server", zap.Error(err), zap.String("region", "us-east"))
	}
}
