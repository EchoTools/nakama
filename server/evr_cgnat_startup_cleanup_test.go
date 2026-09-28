package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// cgnatStartupHarness drives runCGNATStartupCleanup with a settings source
// the test controls, a settings-loaded signal the test closes, and a cleanup
// double that counts its calls. No database, no NakamaModule.
//
// Every test runs inside a synctest bubble: synctest.Wait returns only once
// the goroutine under test has exited or is durably blocked, so "the cleanup
// goroutine started before settings loaded" is an ordering the test enforces,
// not one a scheduler happens to produce (AGENTS.md defect class 2). The wait
// bound runs on the bubble's fake clock.
type cgnatStartupHarness struct {
	settings     atomic.Pointer[ServiceSettingsData]
	loaded       chan struct{}
	logger       *captureLogger
	calls        atomic.Int32
	broken       int
	affected     int
	cleanupErr   error
	settingsWait time.Duration
}

func newCGNATStartupHarness() *cgnatStartupHarness {
	h := &cgnatStartupHarness{
		loaded:       make(chan struct{}),
		logger:       newCaptureLogger(),
		settingsWait: 5 * time.Minute,
	}
	// Before the first load, ServiceSettings() returns a zero struct.
	h.settings.Store(&ServiceSettingsData{})
	return h
}

// load publishes settings and then closes the loaded signal, in the order
// ServiceSettingsLoad does.
func (h *cgnatStartupHarness) load(cleanupOnStartup bool) {
	h.settings.Store(&ServiceSettingsData{CGNAT: CGNATSettings{CleanupOnStartup: cleanupOnStartup}})
	close(h.loaded)
}

func (h *cgnatStartupHarness) deps() cgnatStartupCleanupDeps {
	return cgnatStartupCleanupDeps{
		logger:         h.logger,
		settings:       h.settings.Load,
		settingsLoaded: h.loaded,
		settingsWait:   h.settingsWait,
		cleanup: func(context.Context) (int, int, error) {
			h.calls.Add(1)
			return h.broken, h.affected, h.cleanupErr
		},
	}
}

// start runs the startup cleanup in its own goroutine, as InitializeEvrRuntimeModule does,
// and returns a channel closed when it returns.
func (h *cgnatStartupHarness) start() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runCGNATStartupCleanup(context.Background(), h.deps())
	}()
	return done
}

func requireReturned(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	default:
		t.Fatal("runCGNATStartupCleanup has not returned")
	}
}

// TestCGNATStartupCleanup_RunsWhenSettingsLoadAfterStart reproduces the boot
// order. InitializeEvrRuntimeModule starts the cleanup goroutine from
// server.NewRuntime (main.go), before NewEvrPipeline does the first
// ServiceSettingsLoad. The goroutine read CleanupOnStartup from the zero
// struct ServiceSettings() returns before any load, saw false, and exited, so
// cleanup_on_startup=true never ran the cleanup.
func TestCGNATStartupCleanup_RunsWhenSettingsLoadAfterStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newCGNATStartupHarness()

		done := h.start()
		synctest.Wait() // the goroutine is running (or has returned) before settings load

		h.load(true)
		synctest.Wait()

		requireReturned(t, done)
		if got := h.calls.Load(); got != 1 {
			t.Fatalf("cleanup ran %d times; settings with cleanup_on_startup=true loaded after the startup goroutine began, and the cleanup must run once", got)
		}
	})
}

// TestCGNATStartupCleanup_RunsWhenSettingsLoadedBeforeStart: the other side of
// the window. Settings already loaded when the goroutine starts; it must not
// wait for a second load.
func TestCGNATStartupCleanup_RunsWhenSettingsLoadedBeforeStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newCGNATStartupHarness()
		h.load(true)

		done := h.start()
		synctest.Wait()

		requireReturned(t, done)
		if got := h.calls.Load(); got != 1 {
			t.Fatalf("cleanup ran %d times with settings loaded before start, want 1", got)
		}
	})
}

// TestCGNATStartupCleanup_LogsCompletionWhenNothingBroken: a cleanup that
// breaks no links still logs its outcome, so silence cannot mean either "ran
// and found nothing" or "never ran".
func TestCGNATStartupCleanup_LogsCompletionWhenNothingBroken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newCGNATStartupHarness()
		h.load(true)

		done := h.start()
		synctest.Wait()
		requireReturned(t, done)

		e, ok := h.logger.find("info", "CGNAT: startup cleanup completed")
		if !ok {
			t.Fatalf("no info %q after a cleanup that broke 0 links; events: %+v", "CGNAT: startup cleanup completed", *h.logger.events)
		}
		if e.fields["broken_links"] != 0 || e.fields["affected_users"] != 0 {
			t.Errorf("completion fields = %v, want broken_links=0 affected_users=0", e.fields)
		}
	})
}

// TestCGNATStartupCleanup_LogsCompletionCounts pins the existing completion
// line and its fields.
func TestCGNATStartupCleanup_LogsCompletionCounts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newCGNATStartupHarness()
		h.broken, h.affected = 7, 4
		h.load(true)

		done := h.start()
		synctest.Wait()
		requireReturned(t, done)

		e, ok := h.logger.find("info", "CGNAT: startup cleanup completed")
		if !ok {
			t.Fatalf("no info %q; events: %+v", "CGNAT: startup cleanup completed", *h.logger.events)
		}
		if e.fields["broken_links"] != 7 || e.fields["affected_users"] != 4 {
			t.Errorf("completion fields = %v, want broken_links=7 affected_users=4", e.fields)
		}
	})
}

// TestCGNATStartupCleanup_LogsSkipWhenOff: cleanup_on_startup=false in the
// loaded settings. The cleanup does not run, and the skip is logged.
func TestCGNATStartupCleanup_LogsSkipWhenOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newCGNATStartupHarness()

		done := h.start()
		synctest.Wait()
		h.load(false)
		synctest.Wait()
		requireReturned(t, done)

		if got := h.calls.Load(); got != 0 {
			t.Errorf("cleanup ran %d times with cleanup_on_startup=false, want 0", got)
		}
		if _, ok := h.logger.find("info", "CGNAT: startup cleanup skipped: cleanup_on_startup is off"); !ok {
			t.Errorf("no info %q; events: %+v", "CGNAT: startup cleanup skipped: cleanup_on_startup is off", *h.logger.events)
		}
	})
}

// TestCGNATStartupCleanup_WarnsOnCleanupError pins the existing failure line:
// a Warn carrying the error, and no completion line.
func TestCGNATStartupCleanup_WarnsOnCleanupError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newCGNATStartupHarness()
		h.cleanupErr = errors.New("synthetic storage list failure")
		h.load(true)

		done := h.start()
		synctest.Wait()
		requireReturned(t, done)

		e, ok := h.logger.find("warn", "CGNAT: startup cleanup failed")
		if !ok {
			t.Fatalf("no warn %q; events: %+v", "CGNAT: startup cleanup failed", *h.logger.events)
		}
		if e.fields["error"] != h.cleanupErr {
			t.Errorf("warn error field = %v, want %v", e.fields["error"], h.cleanupErr)
		}
		if _, ok := h.logger.find("info", "CGNAT: startup cleanup completed"); ok {
			t.Error("completion logged for a cleanup that failed")
		}
	})
}

// TestCGNATStartupCleanup_WarnsWhenSettingsNeverLoad: settings never load
// within the bound. The goroutine gives up after settingsWait, logs a Warn,
// and does not run the cleanup. The settings source reports
// cleanup_on_startup=true throughout to show the decision waits for the load
// signal, not for whatever the source says at the time. (No production caller
// publishes settings before the first load today; see serviceSettingsLoaded.)
func TestCGNATStartupCleanup_WarnsWhenSettingsNeverLoad(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newCGNATStartupHarness()
		h.settings.Store(&ServiceSettingsData{CGNAT: CGNATSettings{CleanupOnStartup: true}})

		start := time.Now()
		done := h.start()
		synctest.Wait()

		if got := h.calls.Load(); got != 0 {
			t.Fatalf("cleanup ran %d times before settings loaded, want 0", got)
		}

		<-done
		if waited := time.Since(start); waited < h.settingsWait {
			t.Errorf("gave up after %v, before the %v bound", waited, h.settingsWait)
		}
		if got := h.calls.Load(); got != 0 {
			t.Errorf("cleanup ran %d times though settings never loaded, want 0", got)
		}
		if _, ok := h.logger.find("warn", "CGNAT: startup cleanup skipped: service settings did not load in time"); !ok {
			t.Errorf("no warn %q; events: %+v", "CGNAT: startup cleanup skipped: service settings did not load in time", *h.logger.events)
		}
	})
}

// TestNewCGNATStartupCleanupDeps_WiresTheLoadSignal: the tests above inject
// their own signal, so they cannot see the production wiring. If the startup
// cleanup waited on any channel other than the one ServiceSettingsLoad closes,
// it would wait out its bound, warn and skip: the original symptom, logged.
func TestNewCGNATStartupCleanupDeps_WiresTheLoadSignal(t *testing.T) {
	logger := newCaptureLogger()
	d := newCGNATStartupCleanupDeps(logger, nil, NewCGNATDetector(nil))

	if d.settingsLoaded != serviceSettingsLoaded.done() {
		t.Error("settingsLoaded is not the signal ServiceSettingsLoad closes")
	}
	if d.settingsWait != cgnatStartupSettingsWait {
		t.Errorf("settingsWait = %v, want cgnatStartupSettingsWait (%v)", d.settingsWait, cgnatStartupSettingsWait)
	}
	if d.logger != logger {
		t.Error("logger is not the one passed in")
	}
	if d.cleanup == nil {
		t.Fatal("cleanup is nil")
	}

	prev := serviceSettings.Load()
	t.Cleanup(func() { serviceSettings.Store(prev) })
	want := &ServiceSettingsData{CGNAT: CGNATSettings{CleanupOnStartup: true}}
	serviceSettings.Store(want)
	if got := d.settings(); got != want {
		t.Error("settings does not read the process-wide service settings")
	}
}
