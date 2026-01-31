// Copyright 2018 The Nakama Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/encoding/protojson"
)

type testMetricsWithCounter struct {
	testMetrics
	searchCappedCount int64
}

func (m *testMetricsWithCounter) MatchmakerSearchCapped(delta int64) {
	atomic.AddInt64(&m.searchCappedCount, delta)
}

func (m *testMetricsWithCounter) GetSearchCappedCount() int64 {
	return atomic.LoadInt64(&m.searchCappedCount)
}

func TestMatchmakerMaxSearchHitsConfigValidation(t *testing.T) {
	consoleLogger := loggerForTest(t)

	cfg := NewConfig(consoleLogger)
	if cfg.GetMatchmaker().MaxSearchHits != 1000 {
		t.Fatalf("expected default MaxSearchHits to be 1000, got %d", cfg.GetMatchmaker().MaxSearchHits)
	}

	invalidValues := []int{0, 1, 5, 9}
	for _, val := range invalidValues {
		if val >= 10 {
			t.Errorf("test error: value %d should be invalid (< 10)", val)
		}
	}

	validValues := []int{10, 100, 1000, 5000}
	for _, val := range validValues {
		if val < 10 {
			t.Errorf("test error: value %d should be valid (>= 10)", val)
		}
	}
}

func TestMatchmakerGuardTriggersAtCap(t *testing.T) {
	EvrRuntimeModuleFns = nil

	if testing.Short() {
		t.Skip("Skipping guard trigger test in short mode")
	}

	core, logs := observer.New(zapcore.WarnLevel)
	observedLogger := zap.New(core)

	cfg := NewConfig(observedLogger)
	cfg.Database.Addresses = []string{"postgres:postgres@localhost:5432/nakama"}
	cfg.Matchmaker.IntervalSec = 1
	cfg.Matchmaker.MaxIntervals = 5
	cfg.Matchmaker.RevPrecision = true
	cfg.Matchmaker.MaxSearchHits = 1000

	var err error
	cfg.Runtime.Path, err = os.MkdirTemp("", "nakama-matchmaker-guard-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cfg.Runtime.Path)

	messageRouter := &testMessageRouter{}
	sessionRegistry := &testSessionRegistry{}
	tracker := &testTracker{}
	metrics := &testMetricsWithCounter{}

	jsonpbMarshaler := &protojson.MarshalOptions{
		UseEnumNumbers:  true,
		EmitUnpopulated: false,
		Indent:          "",
		UseProtoNames:   true,
	}
	jsonpbUnmarshaler := &protojson.UnmarshalOptions{
		DiscardUnknown: false,
	}

	_, _, err = createTestMatchRegistry(t, observedLogger)
	if err != nil {
		t.Fatalf("error creating test match registry: %v", err)
	}

	runtime, _, err := NewRuntime(context.Background(), observedLogger, observedLogger, nil, jsonpbMarshaler, jsonpbUnmarshaler, cfg, "", nil, nil, nil, nil, sessionRegistry, nil, nil, nil, tracker, metrics, nil, messageRouter, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	runtime.matchmakerMatchedFunction = func(ctx context.Context, entries []*MatchmakerEntry) (string, bool, error) {
		return "", false, nil
	}

	matchMaker := NewLocalMatchmaker(observedLogger, observedLogger, cfg, messageRouter, metrics, runtime).(*LocalMatchmaker)

	ticketCount := 1500
	t.Logf("Creating %d tickets (cap is %d)...", ticketCount, cfg.GetMatchmaker().MaxSearchHits)

	for i := 0; i < ticketCount; i++ {
		sessionID, _ := uuid.NewV4()
		uniqueQuery := fmt.Sprintf("properties.user_id:user_%d", i)
		_, _, err := matchMaker.Add(context.Background(), []*MatchmakerPresence{
			{
				UserId:    fmt.Sprintf("user_%d", i),
				SessionId: fmt.Sprintf("session_%d", i),
				Username:  fmt.Sprintf("username_%d", i),
				Node:      "node1",
				SessionID: sessionID,
			},
		}, sessionID.String(), "",
			uniqueQuery,
			2, 2, 1,
			map[string]string{"user_id": fmt.Sprintf("user_%d", i)},
			map[string]float64{})
		if err != nil {
			t.Fatalf("error adding ticket %d: %v", i, err)
		}
	}

	matchMaker.Lock()
	inactiveCount := 200
	processed := 0
	for _, index := range matchMaker.indexes {
		if processed >= inactiveCount {
			break
		}
		index.Intervals = cfg.GetMatchmaker().MaxIntervals
		processed++
	}

	matchMaker.activeIndexes = make(map[string]*MatchmakerIndex)
	for ticket, index := range matchMaker.indexes {
		if index.Intervals < cfg.GetMatchmaker().MaxIntervals {
			matchMaker.activeIndexes[ticket] = index
		}
	}
	totalIndexes := len(matchMaker.indexes)
	activeIndexes := len(matchMaker.activeIndexes)
	matchMaker.Unlock()

	t.Logf("Total indexes: %d, active: %d", totalIndexes, activeIndexes)

	timeout := 10 * time.Second
	done := make(chan struct{})
	start := time.Now()

	go func() {
		matchMaker.Process()
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		t.Logf("Process() completed in %v", elapsed)

		if elapsed > timeout {
			t.Errorf("Process() took %v, exceeding timeout of %v", elapsed, timeout)
		}

		cappedCount := metrics.GetSearchCappedCount()
		if cappedCount == 0 {
			t.Errorf("expected MatchmakerSearchCapped metric to be incremented, got 0")
		}
		t.Logf("MatchmakerSearchCapped called %d times", cappedCount)

		warningLogs := logs.FilterMessage("matchmaker search capped").All()
		if len(warningLogs) == 0 {
			t.Errorf("expected warning log 'matchmaker search capped', but none found")
		}
		t.Logf("Found %d warning logs about search capping", len(warningLogs))

	case <-time.After(timeout):
		t.Fatalf("Process() exceeded timeout of %v - guard may not be working", timeout)
	}
}

func TestMatchmakerGuardDoesNotTriggerBelowCap(t *testing.T) {
	EvrRuntimeModuleFns = nil

	if testing.Short() {
		t.Skip("Skipping guard test in short mode")
	}

	core, logs := observer.New(zapcore.WarnLevel)
	observedLogger := zap.New(core)

	cfg := NewConfig(observedLogger)
	cfg.Database.Addresses = []string{"postgres:postgres@localhost:5432/nakama"}
	cfg.Matchmaker.IntervalSec = 1
	cfg.Matchmaker.MaxIntervals = 5
	cfg.Matchmaker.RevPrecision = true
	cfg.Matchmaker.MaxSearchHits = 1000

	var err error
	cfg.Runtime.Path, err = os.MkdirTemp("", "nakama-matchmaker-guard-below-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cfg.Runtime.Path)

	messageRouter := &testMessageRouter{}
	sessionRegistry := &testSessionRegistry{}
	tracker := &testTracker{}
	metrics := &testMetricsWithCounter{}

	jsonpbMarshaler := &protojson.MarshalOptions{
		UseEnumNumbers:  true,
		EmitUnpopulated: false,
		Indent:          "",
		UseProtoNames:   true,
	}
	jsonpbUnmarshaler := &protojson.UnmarshalOptions{
		DiscardUnknown: false,
	}

	_, _, err = createTestMatchRegistry(t, observedLogger)
	if err != nil {
		t.Fatalf("error creating test match registry: %v", err)
	}

	runtime, _, err := NewRuntime(context.Background(), observedLogger, observedLogger, nil, jsonpbMarshaler, jsonpbUnmarshaler, cfg, "", nil, nil, nil, nil, sessionRegistry, nil, nil, nil, tracker, metrics, nil, messageRouter, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	runtime.matchmakerMatchedFunction = func(ctx context.Context, entries []*MatchmakerEntry) (string, bool, error) {
		return "", false, nil
	}

	matchMaker := NewLocalMatchmaker(observedLogger, observedLogger, cfg, messageRouter, metrics, runtime).(*LocalMatchmaker)

	ticketCount := 50
	t.Logf("Creating %d tickets (well below cap of %d)...", ticketCount, cfg.GetMatchmaker().MaxSearchHits)

	for i := 0; i < ticketCount; i++ {
		sessionID, _ := uuid.NewV4()
		_, _, err := matchMaker.Add(context.Background(), []*MatchmakerPresence{
			{
				UserId:    fmt.Sprintf("user_%d", i),
				SessionId: fmt.Sprintf("session_%d", i),
				Username:  fmt.Sprintf("username_%d", i),
				Node:      "node1",
				SessionID: sessionID,
			},
		}, sessionID.String(), "",
			"*",
			2, 2, 1,
			map[string]string{},
			map[string]float64{})
		if err != nil {
			t.Fatalf("error adding ticket %d: %v", i, err)
		}
	}

	matchMaker.Lock()
	totalIndexes := len(matchMaker.indexes)
	activeIndexes := len(matchMaker.activeIndexes)
	matchMaker.Unlock()

	t.Logf("Total indexes: %d, active: %d", totalIndexes, activeIndexes)

	matchMaker.Process()

	cappedCount := metrics.GetSearchCappedCount()
	if cappedCount != 0 {
		t.Errorf("expected MatchmakerSearchCapped metric to be 0, got %d", cappedCount)
	}

	warningLogs := logs.FilterMessage("matchmaker search capped").All()
	if len(warningLogs) > 0 {
		t.Errorf("expected no warning logs about search capping, but found %d", len(warningLogs))
	}

	t.Logf("Guard correctly did not trigger for %d tickets (below cap)", ticketCount)
}
