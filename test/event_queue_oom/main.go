// event_queue_oom — Reproduction for the evr.292 OOM root cause.
//
// Simulates the EventDispatcher queue behavior under production RemoteLogSet
// load. Compares queue=100 (evr.280 behavior) vs queue=4096 (evr.292 behavior).
//
// The hypothesis: with queue=4096, retained JSON payloads accumulate to GB-level
// heap usage. With queue=100, events are dropped under pressure, bounding memory.
//
// Usage:
//   go run . -queue 4096 -duration 5m -rate 2.4
//   go run . -queue 100 -duration 5m -rate 2.4
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"
)

// Simulates the *api.Event that SendEvent produces
type event struct {
	Name       string
	Properties map[string]string
}

// Simulates the EventRemoteLogSet payload that gets json.Marshal'd
type remoteLogSetPayload struct {
	Node      string                 `json:"node"`
	UserID    string                 `json:"user_id"`
	SessionID string                 `json:"session_id"`
	Username  string                 `json:"username"`
	XPID      map[string]interface{} `json:"xpid"`
	Logs      []string               `json:"logs"`
}

func main() {
	queueSize := flag.Int("queue", 4096, "event queue capacity (100=evr.280, 4096=evr.292)")
	duration := flag.Duration("duration", 5*time.Minute, "test duration")
	rate := flag.Float64("rate", 2.4, "events per second (production: ~2.4)")
	logEntries := flag.Int("logs", 12, "average log entries per RemoteLogSet")
	logSize := flag.Int("logsize", 2000, "average bytes per log entry")
	gogc := flag.Int("gogc", 100, "GOGC value")
	memlimit := flag.Int64("memlimit", 0, "GOMEMLIMIT in MB (0=no limit)")
	flag.Parse()

	// Match production GC settings
	debug.SetGCPercent(*gogc)
	if *memlimit > 0 {
		debug.SetMemoryLimit(*memlimit * 1024 * 1024)
	}

	fmt.Printf("=== Event Queue OOM Reproduction ===\n")
	fmt.Printf("Queue capacity: %d\n", *queueSize)
	fmt.Printf("Duration: %v\n", *duration)
	fmt.Printf("Production rate: %.1f events/sec\n", *rate)
	fmt.Printf("Avg log entries: %d, avg entry size: %d bytes\n", *logEntries, *logSize)
	fmt.Printf("GOGC: %d, GOMEMLIMIT: %d MB\n", *gogc, *memlimit)
	fmt.Printf("========================================\n\n")

	queue := make(chan *event, *queueSize)
	var produced, consumed, dropped, timedOut, activeGoroutines atomic.Int64

	// Consumer loop — mirrors server/evr_runtime_events.go:113-149 exactly
	go func() {
		for evt := range queue {
			doneCh := make(chan struct{}, 1)

			activeGoroutines.Add(1)
			go func(e *event) {
				defer activeGoroutines.Add(-1)
				defer close(doneCh)

				// Simulate variable processing time matching production behavior:
				// - RemoteLogSet.Process() iterates over log entries
				// - Writes to leaderboards (bluge index operations)
				// - Match data journal updates
				// - VOIP loudness processing
				// 70% finish in <1s, 20% take 1-3s, 10% take >5s (triggers timeout)
				r := rand.Float64()
				var processingTime time.Duration
				switch {
				case r < 0.70:
					processingTime = time.Duration(100+rand.Intn(900)) * time.Millisecond
				case r < 0.90:
					processingTime = time.Duration(1000+rand.Intn(2000)) * time.Millisecond
				default:
					processingTime = time.Duration(5000+rand.Intn(10000)) * time.Millisecond
				}
				time.Sleep(processingTime)

				// Access payload to prevent compiler optimization
				_ = e.Properties["payload"]
				consumed.Add(1)
			}(evt)

			// Wait up to 5 seconds, then move on (goroutine keeps running)
			select {
			case <-doneCh:
				// processed in time
			case <-time.After(5 * time.Second):
				timedOut.Add(1)
				// goroutine still running, still holding references to evt
			}
		}
	}()

	// Producer — mirrors processRemoteLogSets -> SendEvent at production rate
	interval := time.Duration(float64(time.Second) / *rate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logTicker := time.NewTicker(10 * time.Second)
	defer logTicker.Stop()

	deadline := time.After(*duration)
	start := time.Now()

	fmt.Printf("%-8s %8s %8s %6s/%6s %8s %8s %8s %8s\n",
		"TIME", "HEAP_MB", "SYS_MB", "QLEN", "QCAP", "PRODUCED", "CONSUMED", "DROPPED", "TIMED_OUT")

	printStats := func(label string) {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		fmt.Printf("%-8s %8d %8d %6d/%6d %8d %8d %8d %8d  goroutines=%d\n",
			label,
			stats.HeapInuse/1024/1024,
			stats.Sys/1024/1024,
			len(queue), *queueSize,
			produced.Load(), consumed.Load(), dropped.Load(), timedOut.Load(),
			runtime.NumGoroutine())
	}

	for {
		select {
		case <-deadline:
			close(queue) // stop consumer
			printStats("FINAL")
			fmt.Printf("\n=== RESULT ===\n")
			fmt.Printf("Queue capacity %d: ", *queueSize)
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			heapMB := stats.HeapInuse / 1024 / 1024
			if heapMB > 1024 {
				fmt.Printf("HEAP %d MB — OOM territory\n", heapMB)
			} else if heapMB > 256 {
				fmt.Printf("HEAP %d MB — elevated but bounded\n", heapMB)
			} else {
				fmt.Printf("HEAP %d MB — healthy\n", heapMB)
			}
			fmt.Printf("Events: %d produced, %d consumed, %d dropped, %d timed out\n",
				produced.Load(), consumed.Load(), dropped.Load(), timedOut.Load())
			os.Exit(0)

		case <-logTicker.C:
			elapsed := time.Since(start).Round(time.Second)
			printStats(elapsed.String())

		case <-ticker.C:
			// Build realistic RemoteLogSet payload
			payload := buildPayload(*logEntries, *logSize)

			// Mirror SendEvent exactly:
			// 1. json.Marshal the entire event (including all log strings)
			payloadBytes, _ := json.Marshal(payload)
			// 2. string(payloadBytes) — THE double-copy that is the #1 allocator
			evt := &event{
				Name: "*server.EventRemoteLogSet",
				Properties: map[string]string{
					"payload": string(payloadBytes),
				},
			}

			// Mirror eventFn: push to queue or drop if full
			select {
			case queue <- evt:
				produced.Add(1)
			default:
				dropped.Add(1)
			}
		}
	}
}

func buildPayload(avgEntries, avgEntrySize int) *remoteLogSetPayload {
	// Vary count: 50%-150% of average
	numLogs := avgEntries/2 + rand.Intn(avgEntries)
	if numLogs < 1 {
		numLogs = 1
	}

	logs := make([]string, numLogs)
	for i := range logs {
		// Each log entry is a JSON object from the game client
		// Vary size: 50%-150% of average
		size := avgEntrySize/2 + rand.Intn(avgEntrySize)
		// Use semi-realistic JSON structure
		logs[i] = fmt.Sprintf(`{"message":"game_event_%d","timestamp":%d,"data":"%s"}`,
			rand.Intn(100),
			time.Now().UnixMilli(),
			strings.Repeat("x", size))
	}

	return &remoteLogSetPayload{
		Node:      "nakama2_us-east",
		UserID:    fmt.Sprintf("user-%d", rand.Intn(600)),
		SessionID: fmt.Sprintf("session-%d", rand.Intn(24000)),
		Username:  fmt.Sprintf("player%d", rand.Intn(600)),
		XPID:      map[string]interface{}{"platform_code": 4, "account_id": rand.Int63()},
		Logs:      logs,
	}
}
