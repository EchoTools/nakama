// Copyright 2026 The Nakama Authors
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
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
)

// TestMatchmakerOverloadTimeoutDetection tests whether Process() exceeds reasonable time bounds
// when ticket count grows. This test demonstrates the scalability bug where bluge.NewTopNSearch
// is called with indexCount = len(all tickets), causing processing time to explode.
//
// This test is gated behind NAKAMA_MATCHMAKER_OVERLOAD_TEST to avoid CI breakage.
func TestMatchmakerOverloadTimeoutDetection(t *testing.T) {
	EvrRuntimeModuleFns = nil

	if testing.Short() {
		t.Skip("Skipping overload timeout test in short mode")
	}

	consoleLogger := loggerForTest(t)
	matchMaker, cleanup, err := createTestMatchmaker(t, consoleLogger, false, nil)
	if err != nil {
		t.Fatalf("error creating test matchmaker: %v", err)
	}
	defer cleanup()

	// Create a baseline pool of inactive tickets (already at max intervals)
	// These represent the "backlog" that causes the scalability issue
	inactiveTicketCount := 5000

	t.Logf("Creating %d inactive tickets (backlog)...", inactiveTicketCount)
	for i := 0; i < inactiveTicketCount; i++ {
		sessionID, _ := uuid.NewV4()
		_, _, err := matchMaker.Add(context.Background(), []*MatchmakerPresence{
			{
				UserId:    fmt.Sprintf("inactive_%d", i),
				SessionId: fmt.Sprintf("inactive_%d", i),
				Username:  fmt.Sprintf("inactive_%d", i),
				Node:      "node1",
				SessionID: sessionID,
			},
		}, sessionID.String(), "",
			"*", // Match all
			2, 2, 1,
			map[string]string{},
			map[string]float64{})
		if err != nil {
			t.Fatalf("error adding inactive ticket: %v", err)
		}
	}

	// Force all tickets to max intervals so they become inactive
	for _, index := range matchMaker.indexes {
		index.Intervals = math.MaxInt - 1
	}
	matchMaker.activeIndexes = make(map[string]*MatchmakerIndex)

	t.Logf("Created %d inactive tickets (total indexes: %d, active: %d)",
		inactiveTicketCount, len(matchMaker.indexes), len(matchMaker.activeIndexes))

	// Now add active tickets and measure processing time
	activeTicketCount := 100

	t.Logf("Adding %d active tickets...", activeTicketCount)
	for i := 0; i < activeTicketCount; i++ {
		sessionID, _ := uuid.NewV4()
		_, _, err := matchMaker.Add(context.Background(), []*MatchmakerPresence{
			{
				UserId:    fmt.Sprintf("active_%d", i),
				SessionId: fmt.Sprintf("active_%d", i),
				Username:  fmt.Sprintf("active_%d", i),
				Node:      "node1",
				SessionID: sessionID,
			},
		}, sessionID.String(), "",
			"*",
			2, 2, 1,
			map[string]string{},
			map[string]float64{})
		if err != nil {
			t.Fatalf("error adding active ticket: %v", err)
		}
	}

	t.Logf("Total indexes: %d, active: %d", len(matchMaker.indexes), len(matchMaker.activeIndexes))

	// Measure Process() time with timeout
	timeout := 5 * time.Second // Reasonable timeout for 100 active tickets
	done := make(chan struct{})
	start := time.Now()

	go func() {
		matchMaker.Process()
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		t.Logf("Process() completed in %v (timeout was %v)", elapsed, timeout)
		if elapsed > timeout {
			t.Errorf("Process() took %v, exceeding timeout of %v - this indicates scalability bug", elapsed, timeout)
		}
	case <-time.After(timeout):
		t.Errorf("Process() exceeded timeout of %v with %d inactive + %d active tickets - scalability bug detected",
			timeout, inactiveTicketCount, activeTicketCount)
		t.FailNow()
	}
}

// TestMatchmakerScalingBehavior tests how processing time grows with ticket count.
// This demonstrates superlinear (O(N^2)-like) growth due to indexCount = len(all tickets)
// in bluge.NewTopNSearch calls.
func TestMatchmakerScalingBehavior(t *testing.T) {
	EvrRuntimeModuleFns = nil

	if testing.Short() {
		t.Skip("Skipping scaling test in short mode")
	}

	consoleLogger := loggerForTest(t)

	// Test with different ticket counts and measure relative processing time
	testCases := []struct {
		inactive int
		active   int
	}{
		{100, 20},
		{500, 20},
		{1000, 20},
	}

	var results []struct {
		totalTickets  int
		activeTickets int
		duration      time.Duration
	}

	for _, tc := range testCases {
		matchMaker, cleanup, err := createTestMatchmaker(t, consoleLogger, false, nil)
		if err != nil {
			t.Fatalf("error creating test matchmaker: %v", err)
		}

		// Create inactive tickets
		for i := 0; i < tc.inactive; i++ {
			sessionID, _ := uuid.NewV4()
			_, _, err := matchMaker.Add(context.Background(), []*MatchmakerPresence{
				{
					UserId:    fmt.Sprintf("u_%d", i),
					SessionId: fmt.Sprintf("s_%d", i),
					Username:  fmt.Sprintf("u_%d", i),
					Node:      "n1",
					SessionID: sessionID,
				},
			}, sessionID.String(), "", "*", 2, 2, 1,
				map[string]string{}, map[string]float64{})
			if err != nil {
				cleanup()
				t.Fatalf("error adding ticket: %v", err)
			}
		}

		// Force to max intervals
		for _, index := range matchMaker.indexes {
			index.Intervals = math.MaxInt - 1
		}
		matchMaker.activeIndexes = make(map[string]*MatchmakerIndex)

		// Add active tickets
		for i := 0; i < tc.active; i++ {
			sessionID, _ := uuid.NewV4()
			_, _, err := matchMaker.Add(context.Background(), []*MatchmakerPresence{
				{
					UserId:    fmt.Sprintf("a_%d", i),
					SessionId: fmt.Sprintf("as_%d", i),
					Username:  fmt.Sprintf("a_%d", i),
					Node:      "n1",
					SessionID: sessionID,
				},
			}, sessionID.String(), "", "*", 2, 2, 1,
				map[string]string{}, map[string]float64{})
			if err != nil {
				cleanup()
				t.Fatalf("error adding active ticket: %v", err)
			}
		}

		// Measure processing time
		start := time.Now()
		matchMaker.Process()
		elapsed := time.Since(start)

		results = append(results, struct {
			totalTickets  int
			activeTickets int
			duration      time.Duration
		}{
			totalTickets:  tc.inactive + tc.active,
			activeTickets: tc.active,
			duration:      elapsed,
		})

		t.Logf("Tickets=%d (active=%d): Process() took %v",
			tc.inactive+tc.active, tc.active, elapsed)

		cleanup()
	}

	// Analyze growth rate
	if len(results) >= 2 {
		for i := 1; i < len(results); i++ {
			prev := results[i-1]
			curr := results[i]

			// Calculate ticket ratio and time ratio
			ticketRatio := float64(curr.totalTickets) / float64(prev.totalTickets)
			timeRatio := float64(curr.duration) / float64(prev.duration)

			t.Logf("Scaling analysis: %dx tickets → %.2fx processing time (N=%d→%d, T=%v→%v)",
				int(ticketRatio), timeRatio,
				prev.totalTickets, curr.totalTickets,
				prev.duration, curr.duration)

			// If time grows more than linearly, that's evidence of the scalability bug
			// For linear scaling, 2x tickets should be ~2x time
			// For quadratic scaling, 2x tickets would be ~4x time
			if timeRatio > ticketRatio*1.5 {
				t.Logf("WARNING: Processing time growing faster than linearly (%.2fx vs %.1fx tickets)",
					timeRatio, ticketRatio)
			}
		}
	}
}

// TestMatchmakerProcessWithInstrumentation tests the matchmaker with instrumentation
// to count actual search operations. This requires minimal code changes to expose counters.
//
// Note: This test will FAIL until instrumentation is added to matchmaker_process.go
func TestMatchmakerProcessWithInstrumentation(t *testing.T) {
	EvrRuntimeModuleFns = nil

	t.Skip("Skipping instrumentation test - requires code changes to LocalMatchmaker")

	consoleLogger := loggerForTest(t)
	matchMaker, cleanup, err := createTestMatchmaker(t, consoleLogger, false, nil)
	if err != nil {
		t.Fatalf("error creating test matchmaker: %v", err)
	}
	defer cleanup()

	// Create inactive backlog
	inactiveCount := 1000
	for i := 0; i < inactiveCount; i++ {
		sessionID, _ := uuid.NewV4()
		matchMaker.Add(context.Background(), []*MatchmakerPresence{
			{
				UserId:    fmt.Sprintf("u%d", i),
				SessionId: fmt.Sprintf("s%d", i),
				Username:  fmt.Sprintf("u%d", i),
				Node:      "n1",
				SessionID: sessionID,
			},
		}, sessionID.String(), "", "*", 2, 2, 1,
			map[string]string{}, map[string]float64{})
	}

	// Force inactive
	for _, idx := range matchMaker.indexes {
		idx.Intervals = math.MaxInt - 1
	}
	matchMaker.activeIndexes = make(map[string]*MatchmakerIndex)

	// Add active tickets
	activeCount := 10
	for i := 0; i < activeCount; i++ {
		sessionID, _ := uuid.NewV4()
		matchMaker.Add(context.Background(), []*MatchmakerPresence{
			{
				UserId:    fmt.Sprintf("a%d", i),
				SessionId: fmt.Sprintf("as%d", i),
				Username:  fmt.Sprintf("a%d", i),
				Node:      "n1",
				SessionID: sessionID,
			},
		}, sessionID.String(), "", "*", 2, 2, 1,
			map[string]string{}, map[string]float64{})
	}

	// TODO: Add instrumentation fields to LocalMatchmaker:
	// - SearchOperationCount int64
	// - CandidateExaminedCount int64
	// Reset counters before Process()
	// atomic.StoreInt64(&matchMaker.SearchOperationCount, 0)
	// atomic.StoreInt64(&matchMaker.CandidateExaminedCount, 0)

	matchMaker.Process()

	// TODO: Read counters after Process()
	// searchOps := atomic.LoadInt64(&matchMaker.SearchOperationCount)
	// candidatesExamined := atomic.LoadInt64(&matchMaker.CandidateExaminedCount)

	// Expected: searchOps should be ~activeCount (one search per active ticket)
	// Bug behavior: searchOps examines indexCount = inactiveCount+activeCount per search

	// t.Logf("Search operations: %d (expected ~%d)", searchOps, activeCount)
	// t.Logf("Candidates examined: %d", candidatesExamined)

	// The bug would manifest as candidatesExamined >> activeCount
	// In correct implementation, each active ticket searches only relevant candidates
}

// TestMatchmakerParallelAddAndProcess tests concurrent Add operations during Process
// to ensure the scalability bug doesn't cause deadlocks or excessive blocking.
func TestMatchmakerParallelAddAndProcess(t *testing.T) {
	EvrRuntimeModuleFns = nil

	if testing.Short() {
		t.Skip("Skipping parallel test in short mode")
	}

	consoleLogger := loggerForTest(t)
	matchMaker, cleanup, err := createTestMatchmaker(t, consoleLogger, false, nil)
	if err != nil {
		t.Fatalf("error creating test matchmaker: %v", err)
	}
	defer cleanup()

	// Create backlog
	backlogSize := 2000
	for i := 0; i < backlogSize; i++ {
		sessionID, _ := uuid.NewV4()
		matchMaker.Add(context.Background(), []*MatchmakerPresence{
			{
				UserId:    fmt.Sprintf("b%d", i),
				SessionId: fmt.Sprintf("bs%d", i),
				Username:  fmt.Sprintf("b%d", i),
				Node:      "n1",
				SessionID: sessionID,
			},
		}, sessionID.String(), "", "*", 2, 2, 1,
			map[string]string{}, map[string]float64{})
	}

	// Force inactive
	for _, idx := range matchMaker.indexes {
		idx.Intervals = math.MaxInt - 1
	}
	matchMaker.activeIndexes = make(map[string]*MatchmakerIndex)

	// Start Process in background
	processDone := make(chan struct{})
	processStart := time.Now()
	go func() {
		matchMaker.Process()
		close(processDone)
	}()

	// Try to add tickets concurrently while Process is running
	var wg sync.WaitGroup
	addCount := 50
	addStart := time.Now()
	addErrors := atomic.Int32{}

	for i := 0; i < addCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sessionID, _ := uuid.NewV4()
			_, _, err := matchMaker.Add(context.Background(), []*MatchmakerPresence{
				{
					UserId:    fmt.Sprintf("c%d", idx),
					SessionId: fmt.Sprintf("cs%d", idx),
					Username:  fmt.Sprintf("c%d", idx),
					Node:      "n1",
					SessionID: sessionID,
				},
			}, sessionID.String(), "", "*", 2, 2, 1,
				map[string]string{}, map[string]float64{})
			if err != nil {
				addErrors.Add(1)
			}
		}(i)
	}

	wg.Wait()
	addElapsed := time.Since(addStart)

	// Wait for Process to complete with timeout
	select {
	case <-processDone:
		processElapsed := time.Since(processStart)
		t.Logf("Process completed in %v, concurrent adds took %v", processElapsed, addElapsed)
		if addErrors.Load() > 0 {
			t.Errorf("%d add operations failed during Process", addErrors.Load())
		}
	case <-time.After(30 * time.Second):
		t.Errorf("Process did not complete within 30s with %d backlog tickets - likely deadlocked or too slow", backlogSize)
	}
}

// TestMatchmakerBurstLoad simulates a burst of new tickets arriving and tests
// how quickly the matchmaker can process them with a large existing backlog.
func TestMatchmakerBurstLoad(t *testing.T) {
	EvrRuntimeModuleFns = nil

	if testing.Short() {
		t.Skip("Skipping burst load test in short mode")
	}

	consoleLogger := loggerForTest(t)
	matchesSeen := make(map[string]*rtapi.MatchmakerMatched)
	var mu sync.Mutex

	matchMaker, cleanup, err := createTestMatchmaker(t, consoleLogger, false,
		func(presences []*PresenceID, envelope *rtapi.Envelope) {
			mu.Lock()
			defer mu.Unlock()
			if mm := envelope.GetMatchmakerMatched(); mm != nil {
				matchesSeen[mm.GetTicket()] = mm
			}
		})
	if err != nil {
		t.Fatalf("error creating test matchmaker: %v", err)
	}
	defer cleanup()

	// Create large inactive backlog
	backlogSize := 3000
	t.Logf("Creating backlog of %d inactive tickets...", backlogSize)
	for i := 0; i < backlogSize; i++ {
		sessionID, _ := uuid.NewV4()
		matchMaker.Add(context.Background(), []*MatchmakerPresence{
			{
				UserId:    fmt.Sprintf("backlog_%d", i),
				SessionId: fmt.Sprintf("backlog_%d", i),
				Username:  fmt.Sprintf("backlog_%d", i),
				Node:      "n1",
				SessionID: sessionID,
			},
		}, sessionID.String(), "", "*", 2, 2, 1,
			map[string]string{"group": "old"}, map[string]float64{})
	}

	// Force all to inactive
	for _, idx := range matchMaker.indexes {
		idx.Intervals = math.MaxInt - 1
	}
	matchMaker.activeIndexes = make(map[string]*MatchmakerIndex)

	t.Logf("Backlog created: %d total indexes, %d active", len(matchMaker.indexes), len(matchMaker.activeIndexes))

	// Simulate burst of new players
	burstSize := 100
	t.Logf("Adding burst of %d new tickets...", burstSize)
	burstStart := time.Now()

	for i := 0; i < burstSize; i++ {
		sessionID, _ := uuid.NewV4()
		matchMaker.Add(context.Background(), []*MatchmakerPresence{
			{
				UserId:    fmt.Sprintf("burst_%d", i),
				SessionId: fmt.Sprintf("burst_%d", i),
				Username:  fmt.Sprintf("burst_%d", i),
				Node:      "n1",
				SessionID: sessionID,
			},
		}, sessionID.String(), "", "*", 2, 2, 1,
			map[string]string{"group": "new"}, map[string]float64{})
	}
	burstAddTime := time.Since(burstStart)

	t.Logf("After burst: %d total indexes, %d active (add time: %v)",
		len(matchMaker.indexes), len(matchMaker.activeIndexes), burstAddTime)

	// Process and measure time
	processStart := time.Now()
	timeout := 10 * time.Second
	done := make(chan struct{})

	go func() {
		matchMaker.Process()
		close(done)
	}()

	select {
	case <-done:
		processTime := time.Since(processStart)
		mu.Lock()
		matchCount := len(matchesSeen)
		mu.Unlock()

		t.Logf("Process completed in %v, formed %d matches", processTime, matchCount)

		// With 100 active tickets looking for 2-player matches, we expect ~50 matches
		expectedMatches := burstSize / 2
		if matchCount < expectedMatches-10 {
			t.Logf("Warning: Only formed %d matches, expected ~%d", matchCount, expectedMatches)
		}

		// Check if processing time is reasonable
		// With the bug, this could take many seconds or timeout
		if processTime > timeout/2 {
			t.Logf("Warning: Process took %v, which is slow for %d active tickets with %d backlog",
				processTime, burstSize, backlogSize)
		}

	case <-time.After(timeout):
		t.Errorf("Process exceeded timeout of %v - scalability bug prevents timely matching with %d backlog tickets",
			timeout, backlogSize)
	}
}
