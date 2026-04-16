package server

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"time"

	"go.uber.org/zap"
)

const maxHeapProfiles = 30

// StartHeapWatch periodically writes heap profiles and logs memory stats.
// It creates dir if needed, writes pprof heap snapshots on each interval,
// and retains only the last 30 profiles.
func StartHeapWatch(logger *zap.Logger, interval time.Duration, dir string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Error("heapwatch: failed to create profile directory", zap.String("dir", dir), zap.Error(err))
		return
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			filename := "heap-" + time.Now().Format("20060102-150405") + ".pb.gz"
			path := filepath.Join(dir, filename)

			if err := writeHeapProfile(path); err != nil {
				logger.Warn("heapwatch: failed to write heap profile", zap.String("file", path), zap.Error(err))
				continue
			}

			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			numGoroutines := runtime.NumGoroutine()

			logger.Info("heap snapshot",
				zap.String("file", filename),
				zap.Uint64("heap_inuse_mb", m.HeapInuse/1024/1024),
				zap.Uint64("heap_sys_mb", m.HeapSys/1024/1024),
				zap.Int("goroutines", numGoroutines),
				zap.Uint64("heap_objects", m.HeapObjects),
			)

			pruneOldProfiles(logger, dir)
		}
	}()
}

func writeHeapProfile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	return pprof.WriteHeapProfile(gw)
}

func pruneOldProfiles(logger *zap.Logger, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var profiles []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".gz" {
			profiles = append(profiles, e.Name())
		}
	}

	if len(profiles) <= maxHeapProfiles {
		return
	}

	sort.Strings(profiles)

	for _, name := range profiles[:len(profiles)-maxHeapProfiles] {
		p := filepath.Join(dir, name)
		if err := os.Remove(p); err != nil {
			logger.Warn("heapwatch: failed to remove old profile", zap.String("file", p), zap.Error(err))
		}
	}
}
