package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestMroundRTT(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		modulus  time.Duration
		expected time.Duration
	}{
		{
			name:     "Test Case 1",
			duration: 12 * time.Millisecond,
			modulus:  5 * time.Millisecond,
			expected: 10 * time.Millisecond,
		},
		{
			name:     "Test Case 2",
			duration: 27 * time.Millisecond,
			modulus:  15 * time.Millisecond,
			expected: 30 * time.Millisecond,
		},
		{
			name:     "Test Case 3",
			duration: 25 * time.Millisecond,
			modulus:  15 * time.Millisecond,
			expected: 30 * time.Millisecond,
		},
		// Add more test cases as needed
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mroundRTT(tt.duration, tt.modulus)
			if result != tt.expected {
				t.Errorf("Expected %v, but got %v", tt.expected, result)
			}
		})
	}
}

func TestRTTweightedPopulationComparison(t *testing.T) {
	tests := []struct {
		name     string
		i        time.Duration
		j        time.Duration
		o        int
		p        int
		expected bool
	}{
		{
			name:     "Test Case 1",
			i:        100 * time.Millisecond,
			j:        80 * time.Millisecond,
			o:        10,
			p:        5,
			expected: true,
		},
		{
			name:     "Test Case 2",
			i:        80 * time.Millisecond,
			j:        100 * time.Millisecond,
			o:        5,
			p:        10,
			expected: false,
		},
		{
			name:     "Test Case 3",
			i:        90 * time.Millisecond,
			j:        90 * time.Millisecond,
			o:        5,
			p:        5,
			expected: false,
		},
		// Add more test cases as needed
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RTTweightedPopulationCmp(tt.i, tt.j, tt.o, tt.p)
			if result != tt.expected {
				t.Errorf("Expected %v, but got %v", tt.expected, result)
			}
		})
	}
}
func TestEvrPipeline_Matchmaking(t *testing.T) {
	ctx := context.Background()
	session := &sessionWS{}
	matchLogger := zap.NewNop()
	msession := &MatchmakingSession{}

	p := &EvrPipeline{}

	t.Run("PingEndpoints returns error", func(t *testing.T) {
		expectedErr := errors.New("ping error")
		p.PingEndpoints = func(ctx context.Context, session *sessionWS, msession *MatchmakingSession, broadcasters []string) ([]PingResult, error) {
			return nil, expectedErr
		}

		_, err := p.MatchMake(ctx, session, matchLogger, msession)
		if err != expectedErr {
			t.Errorf("Expected error: %v, but got: %v", expectedErr, err)
		}
	})

	t.Run("BuildMatchmakingQuery returns error", func(t *testing.T) {
		expectedErr := errors.New("build query error")
		p.PingEndpoints = func(ctx context.Context, session *sessionWS, msession *MatchmakingSession, broadcasters []string) ([]PingResult, error) {
			return []PingResult{}, nil
		}
		p.BuildMatchmakingQuery = func(ctx context.Context, latencies map[string]BroadcasterLatencies, session *sessionWS, msession *MatchmakingSession) (string, map[string]string, map[string]float64, error) {
			return "", nil, nil, expectedErr
		}

		_, err := p.MatchMake(ctx, session, matchLogger, msession)
		if err != expectedErr {
			t.Errorf("Expected error: %v, but got: %v", expectedErr, err)
		}
	})

	t.Run("Add returns error", func(t *testing.T) {
		expectedErr := errors.New("add error")
		p.PingEndpoints = func(ctx context.Context, session *sessionWS, msession *MatchmakingSession, broadcasters []string) ([]PingResult, error) {
			return []PingResult{}, nil
		}
		p.BuildMatchmakingQuery = func(ctx context.Context, latencies map[string]BroadcasterLatencies, session *sessionWS, msession *MatchmakingSession) (string, map[string]string, map[string]float64, error) {
			return "", nil, nil, nil
		}
		session.matchmaker.Add = func(ctx context.Context, presences []*MatchmakerPresence, sessionID, partyID, query string, minCount, maxCount, countMultiple int, stringProps map[string]string, numericProps map[string]float64) (string, int, error) {
			return "", 0, expectedErr
		}

		_, err := p.MatchMake(ctx, session, matchLogger, msession)
		if err != expectedErr {
			t.Errorf("Expected error: %v, but got: %v", expectedErr, err)
		}
	})

	t.Run("Successful matchmake", func(t *testing.T) {
		p.PingEndpoints = func(ctx context.Context, session *sessionWS, msession *MatchmakingSession, broadcasters []string) ([]PingResult, error) {
			return []PingResult{}, nil
		}
		p.BuildMatchmakingQuery = func(ctx context.Context, latencies map[string]BroadcasterLatencies, session *sessionWS, msession *MatchmakingSession) (string, map[string]string, map[string]float64, error) {
			return "", nil, nil, nil
		}
		session.matchmaker.Add = func(ctx context.Context, presences []*MatchmakerPresence, sessionID, partyID, query string, minCount, maxCount, countMultiple int, stringProps map[string]string, numericProps map[string]float64) (string, int, error) {
			return "ticket123", 0, nil
		}

		ticket, err := p.MatchMake(ctx, session, matchLogger, msession)
		if err != nil {
			t.Errorf("Expected no error, but got: %v", err)
		}
		if ticket != "ticket123" {
			t.Errorf("Expected ticket: %s, but got: %s", "ticket123", ticket)
		}
	})
}
