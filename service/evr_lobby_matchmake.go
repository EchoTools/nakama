package service

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
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
	MatchmakerStorageCollection      = "server.Matchmaker"
	MatchmakerLatestCandidatesKey    = "latestCandidates"
	MatchmakingConfigStorageKey      = "config"
)

type MatchmakingTicketParameters struct {
	MinCount                   int
	MaxCount                   int
	CountMultiple              int
	IncludeSBMMRanges          bool
	IncludeEarlyQuitPenalty    bool
	IncludeRequireCommonServer bool
}

func (m *MatchmakingTicketParameters) MarshalText() ([]byte, error) {
	// encode it as minCount/maxCount/countMultiple/includeRankRange/includeEarlyQuitPenalty
	s := fmt.Sprintf("%d/%d/%d/%t/%t", m.MinCount, m.MaxCount, m.CountMultiple, m.IncludeSBMMRanges, m.IncludeEarlyQuitPenalty)
	return []byte(s), nil
}

func (m *MatchmakingTicketParameters) UnmarshalText(text []byte) error {

	parts := strings.Split(string(text), "/")
	if len(parts) != 5 {
		return fmt.Errorf("invalid MatchmakingTicketParameters format")
	}

	minCount, err := strconv.Atoi(parts[0])
	if err != nil {
		return err
	}

	maxCount, err := strconv.Atoi(parts[1])
	if err != nil {
		return err
	}

	countMultiple, err := strconv.Atoi(parts[2])
	if err != nil {
		return err
	}

	includeRankRange := parts[3] == "true"
	includeEarlyQuitPenalty := parts[4] == "true"

	m.MinCount = minCount
	m.MaxCount = maxCount
	m.CountMultiple = countMultiple
	m.IncludeSBMMRanges = includeRankRange
	m.IncludeEarlyQuitPenalty = includeEarlyQuitPenalty

	return nil
}

var DefaultMatchmakerTicketConfigs = map[evr.Symbol]MatchmakingTicketParameters{
	evr.ModeArenaPublic: {
		MinCount:                8,
		MaxCount:                8,
		CountMultiple:           2,
		IncludeSBMMRanges:       true,
		IncludeEarlyQuitPenalty: true,
	},
	evr.ModeCombatPublic: {
		MinCount:                8,
		MaxCount:                10,
		CountMultiple:           2,
		IncludeSBMMRanges:       false,
		IncludeEarlyQuitPenalty: false,
	},
}

func (p *EvrPipeline) matchmakingTicketTimeout() time.Duration {
	maxIntervals := p.config.GetMatchmaker().MaxIntervals
	intervalSecs := p.config.GetMatchmaker().IntervalSec
	return time.Duration(maxIntervals*intervalSecs) * time.Second
}

func (p *EvrPipeline) lobbyMatchMakeWithFallback(ctx context.Context, logger *zap.Logger, session *sessionEVR, lobbyParams *LobbySessionParameters, partyLabel *PartyLabel, entrants ...*EvrMatchPresence) (err error) {

	stream := lobbyParams.GuildGroupStream()
	count, err := p.nk.StreamCount(stream.Mode, stream.Subject.String(), "", stream.Label)
	if err != nil {
		logger.Error("Failed to get stream count", zap.Error(err))
	}

	// If there are fewer players online, reduce the fallback delay

	var (
		mmInterval               = time.Duration(p.config.GetMatchmaker().IntervalSec) * time.Second
		matchmakingTicketTimeout = time.Duration(p.config.GetMatchmaker().MaxIntervals) * mmInterval
		timeoutTimer             = time.NewTimer(lobbyParams.MatchmakingTimeout)
		fallbackTimer            = time.NewTimer(min(lobbyParams.FallbackTimeout, matchmakingTicketTimeout-mmInterval))
		ticketTicker             = time.NewTicker(matchmakingTicketTimeout)
		tickets                  = make([]string, 0, 2)
	)

	ticketConfig, ok := DefaultMatchmakerTicketConfigs[lobbyParams.Mode]
	if !ok {
		return fmt.Errorf("matchmaking ticket config not found for mode %s", lobbyParams.Mode)
	}

	if !strings.Contains(p.node, "dev") {
		// If there are fewer than 16 players online, reduce the fallback delay
		if count < 24 {
			ticketConfig.IncludeSBMMRanges = false
			ticketConfig.IncludeEarlyQuitPenalty = false
		}
	}
	defer func() {
		session.matchmaker.Remove(tickets)
	}()

	cycle := 0
	for {

		if ticket, err := p.addTicket(ctx, logger, session, lobbyParams, partyLabel, ticketConfig); err != nil {
			return fmt.Errorf("failed to add ticket: %w", err)
		} else {
			tickets = append(tickets, ticket)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-timeoutTimer.C:
			logger.Debug("Matchmaking timeout")
			return ErrMatchmakingTimeout
		case <-ticketTicker.C:
			logger.Debug("Matchmaking ticket timeout", zap.Int("cycle", cycle))
		case <-fallbackTimer.C:
			logger.Debug("Matchmaking fallback")

			// add a ticket with a smaller count, and no rank range
			//ticketConfig.IncludeSBMMRanges = false
			ticketConfig.IncludeEarlyQuitPenalty = false
			ticketConfig.MinCount = 2
			ticketConfig.MaxCount = 8
			if ticket, err := p.addTicket(ctx, logger, session, lobbyParams, partyLabel, ticketConfig); err != nil {
				return fmt.Errorf("failed to add ticket: %w", err)
			} else {
				tickets = append(tickets, ticket)
			}
		}
		cycle++
	}
}

func (p *EvrPipeline) addTicket(ctx context.Context, logger *zap.Logger, session *sessionEVR, lobbyParams *LobbySessionParameters, partyLabel *PartyLabel, ticketConfig MatchmakingTicketParameters) (string, error) {
	var err error

	query, stringProps, numericProps := lobbyParams.MatchmakingParameters(&ticketConfig)

	// The matchmaker will always prioritize the players that are about to timeout.
	priorityThreshold := time.Now().UTC().Add((p.matchmakingTicketTimeout() / 3) * 2)

	stringProps["priority_threshold"] = priorityThreshold.UTC().Format(time.RFC3339)

	minCount := ticketConfig.MinCount
	maxCount := ticketConfig.MaxCount
	countMultiple := ticketConfig.CountMultiple

	ticket := ""
	otherPresences := []*server.PresenceID{}
	sessionID := session.ID().String()
	if partyLabel != nil && partyLabel.Size() > 1 {

		// Matchmake with the lobby group via the party handler.
		ticket, otherPresences, err = p.partyRegistry.PartyMatchmakerAdd(ctx, partyLabel.ID, p.node, sessionID, p.node, query, minCount, maxCount, countMultiple, stringProps, numericProps)
		if err != nil {
			return "", fmt.Errorf("failed to add party matchmaker ticket: %w", err)
		}

	} else {
		// This is a solo matchmaker.
		presences := []*server.MatchmakerPresence{
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
			return "", fmt.Errorf("failed to add solo matchmaker ticket: %w", err)
		}

	}

	logger.Debug("Matchmaking ticket added", zap.String("query", query), zap.Any("string_properties", stringProps), zap.Any("numeric_properties", numericProps), zap.Any("ticket_config", ticketConfig), zap.String("ticket", ticket), zap.Any("presences", otherPresences))

	return ticket, nil
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
	DisableArenaBackfill     bool     `json:"disable_arena_backfill"`    // Disable backfilling for arena matches
	BackfillQueryAddon       string   `json:"backfill_query_addon"`      // Additional query to add to the matchmaking query
	LobbyBuilderQueryAddon   string   `json:"lobby_builder_query_addon"` // Additional query to add to the matchmaking query
	CreateQueryAddon         string   `json:"create_query_addon"`        // Additional query to add to the matchmaking query
	MatchmakerQueryAddon     string   `json:"matchmaker_query_addon"`    // Additional query to add to the matchmaking query
	LobbyGroupName           string   `json:"group_id"`                  // Group ID to matchmake with
	NextMatchID              MatchID  `json:"next_match_id"`             // Try to join this match immediately when finding a match
	NextMatchRole            string   `json:"next_match_role"`           // The role to join the next match as
	NextMatchDiscordID       string   `json:"next_match_discord_id"`     // The discord ID to join the next match as
	StaticBaseRankPercentile float64  `json:"static_rank_percentile"`    // The static rank percentile to use
	Divisions                []string `json:"divisions"`                 // The division to use
	ExcludedDivisions        []string `json:"excluded_divisions"`        // The division to use
}

func (MatchmakingSettings) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection: MatchmakerStorageCollection,
		Key:        MatchmakingConfigStorageKey,
	}
}

func (m MatchmakingSettings) SetStorageMeta(meta StorableMetadata) {
	// MatchmakingSettings doesn't track version, so nothing to set
}

func (MatchmakingSettings) StorageIndexes() []StorableIndexMeta {
	return []StorableIndexMeta{{
		Name:           ActivePartyGroupIndex,
		Collection:     MatchmakerStorageCollection,
		Key:            MatchmakingConfigStorageKey,
		Fields:         []string{"group_id"},
		SortableFields: nil,
		MaxEntries:     100000,
		IndexOnly:      false,
	}}
}

func LoadMatchmakingSettings(ctx context.Context, nk runtime.NakamaModule, userID string) (settings MatchmakingSettings, err error) {
	err = StorableRead(ctx, nk, userID, &settings, true)
	return
}

func StoreMatchmakingSettings(ctx context.Context, nk runtime.NakamaModule, userID string, settings MatchmakingSettings) error {
	err := StorableWrite(ctx, nk, userID, settings)
	return err
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
	k := RTTPropertyPrefix + e.Endpoint.ExternalIP.String()
	v := float64(e.RTT / time.Millisecond)
	return k, v
}
