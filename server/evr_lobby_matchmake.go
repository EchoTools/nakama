package server

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

const (
	LatencyCacheRefreshInterval = time.Hour * 3
	LatencyCacheExpiry          = time.Hour * 72 // 3 hours

	LatencyCacheStorageKey = "LatencyCache"
)

const (
	MatchmakingStartGracePeriod = 3 * time.Second
	MadeMatchBackfillDelay      = 15 * time.Second
)

var (
	ErrMatchmakingPingTimeout        = NewLobbyErrorf(Timeout, "Ping timeout")
	ErrMatchmakingTimeout            = NewLobbyErrorf(Timeout, "Matchmaking timeout")
	ErrMatchmakingNoAvailableServers = NewLobbyError(ServerFindFailed, "No available servers")
	ErrMatchmakingCanceled           = NewLobbyErrorf(BadRequest, "Matchmaking canceled")
	ErrMatchmakingCanceledByPlayer   = NewLobbyErrorf(BadRequest, "Matchmaking canceled by player")
	ErrMatchmakingCanceledByParty    = NewLobbyErrorf(BadRequest, "Matchmaking canceled by party member")
	ErrMatchmakingRestarted          = NewLobbyErrorf(BadRequest, "matchmaking restarted")
	ErrMatchmakingUnknownError       = NewLobbyErrorf(InternalError, "Unknown error")
	MatchmakingStreamSubject         = uuid.NewV5(uuid.Nil, "matchmaking").String()
	MatchmakerStorageCollection      = "Matchmaker"
	MatchmakerLatestCandidatesKey    = "latestCandidates"
	MatchmakingConfigStorageKey      = "config"
)

type MatchmakerTicketConfig struct {
	MinCount                int
	MaxCount                int
	CountMultiple           int
	includeRankRange        bool
	includeEarlyQuitPenalty bool
}

var DefaultMatchmakerTicketConfigs = map[evr.Symbol]MatchmakerTicketConfig{
	evr.ModeArenaPublic: {
		MinCount:      2,
		MaxCount:      8,
		CountMultiple: 2,
	},
	evr.ModeCombatPublic: {
		MinCount:      2,
		MaxCount:      10,
		CountMultiple: 2,
	},
}

func (p *EvrPipeline) matchmakingTicketTimeout() time.Duration {
	maxIntervals := p.config.GetMatchmaker().MaxIntervals
	intervalSecs := p.config.GetMatchmaker().IntervalSec
	return time.Duration(maxIntervals*intervalSecs) * time.Second
}

func (p *EvrPipeline) lobbyMatchMakeWithFallback(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters, lobbyGroup *LobbyGroup) (err error) {

	ticketConfig, ok := DefaultMatchmakerTicketConfigs[lobbyParams.Mode]
	if !ok {
		return fmt.Errorf("matchmaking ticket config not found for mode %s", lobbyParams.Mode)
	}

	ticketConfig.includeRankRange = true
	ticketConfig.includeEarlyQuitPenalty = true

	// Add the primary ticket
	err = p.addTicket(ctx, logger, session, lobbyParams, lobbyGroup, ticketConfig)
	if err != nil {
		return fmt.Errorf("failed to add primary ticket: %v", err)
	}

	// Start the fallback when half the matchmaking timeout has expired
	fallbackDelay := p.matchmakingTicketTimeout() / 2

	// Reduce fallback delay if there is very few players online
	stream := lobbyParams.GuildGroupStream()
	count, err := p.runtimeModule.StreamCount(stream.Mode, stream.Subject.String(), "", stream.Label)
	if err != nil {
		logger.Error("Failed to get stream count", zap.Error(err))
	} else {

		// If there are fewer players online, reduce the fallback delay
		if !strings.Contains(p.node, "dev") {
			// If there are fewer than 16 players online, reduce the fallback delay
			if count < 16 {
				fallbackDelay = min(p.matchmakingTicketTimeout()/2, 4*time.Minute)
			} else if count < 8 {
				fallbackDelay = 1 * time.Minute
			}
		}
	}

	// If the first matchmaking ticket fails, try a fallback ticket
	go func() {

		// Check if the context was canceled
		select {
		case <-ctx.Done():
			return
		case <-time.After(fallbackDelay):
		}

		// Attempt a fallback ticket
		ticketConfig.MaxCount = ticketConfig.MinCount
		ticketConfig.MinCount = 1 // This must be 1 to allow for the fallback to work with nakama's matchmaker (min must always be less than max)
		ticketConfig.includeRankRange = false
		ticketConfig.includeEarlyQuitPenalty = false

		if ticketConfig.CountMultiple == 1 || ticketConfig.MaxCount == ticketConfig.MinCount || ticketConfig.MaxCount%ticketConfig.CountMultiple != 0 {
			logger.Error("Matchmaking ticket config is not valid for fallbacks", zap.Any("config", ticketConfig))
			return
		}

		// This will try to add a secondary ticket with the smallest count.
		// This works around a nakama limitation of using a custom matchmaker.

		err = p.addTicket(ctx, logger, session, lobbyParams, lobbyGroup, ticketConfig)
		if err != nil {
			logger.Error("Failed to add secondary ticket", zap.Error(err))
			return
		}
	}()
	return nil
}

func (p *EvrPipeline) addTicket(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters, lobbyGroup *LobbyGroup, ticketConfig MatchmakerTicketConfig) error {
	var err error
	sessionParams, ok := LoadParams(ctx)
	if !ok {
		return fmt.Errorf("failed to load session parameters")
	}

	query, stringProps, numericProps := lobbyParams.MatchmakingParameters(sessionParams, ticketConfig.includeRankRange, ticketConfig.includeEarlyQuitPenalty)

	// now + 2/3 matchmaking timeout
	timeout := (p.matchmakingTicketTimeout() * 2) / 3
	priorityThreshold := time.Now().UTC().Add(timeout).Unix()

	numericProps["priority_threshold"] = float64(priorityThreshold)

	minCount := ticketConfig.MinCount
	maxCount := ticketConfig.MaxCount
	countMultiple := ticketConfig.CountMultiple

	stringProps["query"] = query

	ticket := ""
	otherPresences := []*PresenceID{}
	sessionID := session.ID().String()
	if lobbyGroup != nil && lobbyGroup.Size() > 1 {

		// Matchmake with the lobby group via the party handler.
		ticket, otherPresences, err = lobbyGroup.MatchmakerAdd(sessionID, session.pipeline.node, query, minCount, maxCount, countMultiple, stringProps, numericProps)
		if err != nil {
			return fmt.Errorf("failed to add party matchmaker ticket: %v", err)
		}

	} else {
		// This is a solo matchmaker.
		presences := []*MatchmakerPresence{
			{
				UserId:    session.UserID().String(),
				SessionId: session.ID().String(),
				Username:  session.Username(),
				Node:      p.node,
				SessionID: session.id,
			},
		}

		// If the user is not in a party, the must submit the ticket through the matchmaker instead of the party handler.
		ticket, _, err = session.matchmaker.Add(ctx, presences, sessionID, "", query, minCount, maxCount, countMultiple, stringProps, numericProps)
		if err != nil {
			logger.Error("Failed to add solo matchmaker ticket", zap.Error(err), zap.String("query", query), zap.Any("string_properties", stringProps), zap.Any("numeric_properties", numericProps))
			return fmt.Errorf("failed to add solo matchmaker ticket: %v", err)
		}

	}
	go func() {
		<-ctx.Done()
		session.matchmaker.Remove([]string{ticket})
	}()

	logger.Debug("Matchmaking ticket added", zap.String("query", query), zap.Any("string_properties", stringProps), zap.Any("numeric_properties", numericProps), zap.String("ticket", ticket), zap.Any("presences", otherPresences))

	return nil
}

// mroundRTT rounds the rtt to the nearest modulus
func mroundRTT[T time.Duration | int](rtt T, modulus T) T {
	if rtt == 0 {
		return 0
	}
	if rtt <= modulus {
		return rtt
	}
	r := float64(rtt) / float64(modulus)
	return T(math.Round(r)) * modulus
}

// LatencyCmp compares by latency, round to the nearest 10ms
func LatencyCmp[T int | time.Duration](i, j T, mround T) bool {
	// Round to the closest 10ms
	return mroundRTT(i, mround) < mroundRTT(j, mround)
}

type MatchmakingSettings struct {
	DisableArenaBackfill        bool     `json:"disable_arena_backfill"`                   // Disable backfilling for arena matches
	BackfillQueryAddon          string   `json:"backfill_query_addon"`                     // Additional query to add to the matchmaking query
	MatchmakingQueryAddon       string   `json:"matchmaking_query_addon"`                  // Additional query to add to the matchmaking query
	CreateQueryAddon            string   `json:"create_query_addon"`                       // Additional query to add to the matchmaking query
	LobbyGroupName              string   `json:"group_id"`                                 // Group ID to matchmake with
	PriorityBroadcasters        []string `json:"priority_broadcasters,omitempty"`          // Prioritize these broadcasters
	NextMatchID                 MatchID  `json:"next_match_id"`                            // Try to join this match immediately when finding a match
	NextMatchRole               string   `json:"next_match_role"`                          // The role to join the next match as
	NextMatchDiscordID          string   `json:"next_match_discord_id"`                    // The discord ID to join the next match as
	MaxServerRTT                int      `json:"max_server_rtt,omitempty"`                 // The maximum RTT to allow
	RankPercentileMaxDelta      float64  `json:"rank_percentile_delta_max,omitempty"`      // The upper limit percentile range to matchmake with
	RankResetSchedule           string   `json:"rank_reset_schedule,omitempty"`            // The reset schedule to use for rankings
	RankPercentileDampingFactor float64  `json:"rank_percentile_damping_factor,omitempty"` // The damping factor to use for rank percentile
	RankInDisplayName           bool     `json:"rank_in_display_name,omitempty"`           // Display the rank in the display name
}

func (MatchmakingSettings) GetStorageID() StorageID {
	return StorageID{
		Collection: MatchmakerStorageCollection,
		Key:        MatchmakingConfigStorageKey,
	}
}

func LoadMatchmakingSettings(ctx context.Context, nk runtime.NakamaModule, userID string) (settings MatchmakingSettings, err error) {
	err = LoadFromStorage(ctx, nk, userID, &settings, true)
	return
}

func StoreMatchmakingSettings(ctx context.Context, nk runtime.NakamaModule, userID string, settings MatchmakingSettings) error {
	return SaveToStorage(ctx, nk, userID, settings)
}

func ipToKey(ip net.IP) string {
	b := ip.To4()
	return fmt.Sprintf("rtt%02x%02x%02x%02x", b[0], b[1], b[2], b[3])
}
func keyToIP(key string) net.IP {
	b, _ := hex.DecodeString(key[3:])
	return net.IPv4(b[0], b[1], b[2], b[3])
}

type LatencyMetric struct {
	Endpoint  evr.Endpoint
	RTT       time.Duration
	Timestamp time.Time
}

// String returns a string representation of the endpoint
func (e *LatencyMetric) String() string {
	return fmt.Sprintf("EndpointRTT(InternalIP=%s, ExternalIP=%s, RTT=%s, Timestamp=%s)", e.Endpoint.InternalIP, e.Endpoint.ExternalIP, e.RTT, e.Timestamp)
}

// ID returns a unique identifier for the endpoint
func (e *LatencyMetric) ID() string {
	return e.Endpoint.GetExternalIP()
}

// The key used for matchmaking properties
func (e *LatencyMetric) AsProperty() (string, float64) {
	k := fmt.Sprintf("rtt%s", ipToKey(e.Endpoint.ExternalIP))
	v := float64(e.RTT / time.Millisecond)
	return k, v
}
