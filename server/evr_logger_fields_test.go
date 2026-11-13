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

// Both the "source" field (runtime.Caller) and zap's own caller must name the
// line that called LogWarn/LogError, not the helper that forwards to the logger.
func TestLogHelpersReportTheCallSite(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	rl := NewRuntimeGoLogger(zap.New(core, zap.AddCaller()))

	_, thisFile, line, _ := goruntime.Caller(0)
	LogWarn(rl, "warn-msg", zap.String("k", "v"))   // line+1
	LogError(rl, "error-msg", zap.String("k", "v")) // line+2

	for i, level := range []zapcore.Level{zapcore.WarnLevel, zapcore.ErrorLevel} {
		entries := logs.FilterLevelExact(level).All()
		require.Len(t, entries, 1)
		want := fmt.Sprintf("%s:%d", thisFile, line+1+i)
		require.Equal(t, want, fmt.Sprintf("%s:%d", entries[0].Caller.File, entries[0].Caller.Line), "%s zap caller", level)
		if level >= zapcore.WarnLevel { // Debug/Info carry no source field
			require.Equal(t, want, entries[0].ContextMap()["source"], "%s source field", level)
		}
		require.Equal(t, "v", entries[0].ContextMap()["k"])
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
