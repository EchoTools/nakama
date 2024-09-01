package server

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	findAttemptsExpiry          = time.Minute * 3
	LatencyCacheRefreshInterval = time.Hour * 3
	LatencyCacheExpiry          = time.Hour * 72 // 3 hours

	MatchmakingStorageCollection = "MatchmakingRegistry"
	LatencyCacheStorageKey       = "LatencyCache"
)

const (
	MatchmakingStartGracePeriod = 3 * time.Second
	MadeMatchBackfillDelay      = 15 * time.Second
)

var (
	ErrMatchmakingPingTimeout          = status.Errorf(codes.DeadlineExceeded, "Ping timeout")
	ErrMatchmakingTimeout              = status.Errorf(codes.DeadlineExceeded, "Matchmaking timeout")
	ErrMatchmakingNoAvailableServers   = status.Errorf(codes.Unavailable, "No available servers")
	ErrMatchmakingCanceled             = status.Errorf(codes.Canceled, "Matchmaking canceled")
	ErrMatchmakingCanceledByPlayer     = status.Errorf(codes.Canceled, "Matchmaking canceled by player")
	ErrMatchmakingCanceledByParty      = status.Errorf(codes.Aborted, "Matchmaking canceled by party member")
	ErrMatchmakingRestarted            = status.Errorf(codes.Canceled, "matchmaking restarted")
	ErrMatchmakingMigrationRequired    = status.Errorf(codes.FailedPrecondition, "Server upgraded, migration")
	ErrMatchmakingUnknownError         = status.Errorf(codes.Unknown, "Unknown error")
	MatchmakingStreamSubject           = uuid.NewV5(uuid.Nil, "matchmaking").String()
	MatchmakingConfigStorageCollection = "Matchmaker"
	MatchmakingConfigStorageKey        = "config"
)

type MatchmakerTicketConfig struct {
	MinCount      int
	MaxCount      int
	CountMultiple int
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

// Matchmake attempts to find/create a match for the user using the nakama matchmaker
func (p *EvrPipeline) lobbyMatchMake(ctx context.Context, logger *zap.Logger, session *sessionWS, params SessionParameters, lobbyGroup *LobbyGroup) (err error) {

	partyList := lobbyGroup.List()
	ratedTeam := make(RatedTeam, 0, len(partyList))
	for _, presence := range partyList {
		rating, err := GetRatinByUserID(ctx, p.db, presence.Presence.GetUserId())
		if err != nil || rating.Mu == 0 || rating.Sigma == 0 || rating.Z == 0 {
			rating = NewDefaultRating()
		}
		ratedTeam = append(ratedTeam, rating)
	}

	query, stringProps, numericProps, err := lobbyMatchmakeQuery(ctx, logger, p.db, session, ratedTeam.Rating(), params)
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to build matchmaking query: %v", err)
	}

	logger.Debug("Matchmaking query", zap.String("query", query), zap.Any("stringProps", stringProps), zap.Any("numericProps", numericProps))

	ticketConfig, ok := DefaultMatchmakerTicketConfigs[params.Mode]
	if !ok {
		return status.Errorf(codes.Internal, "Matchmaking ticket config not found for mode %s", params.Mode)
	}

	minCount := ticketConfig.MinCount
	maxCount := ticketConfig.MaxCount
	countMultiple := ticketConfig.CountMultiple

	ticket := ""
	otherPresences := []*PresenceID{}

	if len(partyList) == 1 {
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
		ticket, _, err = session.matchmaker.Add(ctx, presences, session.ID().String(), "", query, minCount, maxCount, countMultiple, stringProps, numericProps)
		if err != nil {
			return status.Errorf(codes.Internal, "Failed to add matchmaker ticket: %v", err)
		}
	} else {
		// Matchmake with the lobby group via the party handler.
		ticket, otherPresences, err = lobbyGroup.MatchmakerAdd(session.id.String(), session.pipeline.node, query, minCount, maxCount, countMultiple, stringProps, numericProps)
		if err != nil {
			return status.Errorf(codes.Internal, "Failed to add matchmaker ticket: %v", err)
		}
	}

	logger.Debug("Matchmaking ticket added", zap.String("ticket", ticket), zap.Any("presences", otherPresences))

	go func() {
		<-ctx.Done()

		err := session.pipeline.matchmaker.RemovePartyAll(lobbyGroup.IDStr())
		if err == nil {
			logger.Debug("Removed matchmaker ticket", zap.String("ticket", ticket))
		}

		err = session.pipeline.matchmaker.RemoveSessionAll(session.id.String())
		if err == nil {
			logger.Debug("Removed matchmaker ticket", zap.String("ticket", ticket))
		}

	}()

	return nil
}

func lobbyMatchmakeQuery(ctx context.Context, logger *zap.Logger, db *sql.DB, session Session, rating types.Rating, params SessionParameters) (query string, stringProps map[string]string, numericProps map[string]float64, err error) {

	stringProps = map[string]string{
		"mode":         params.Mode.String(),
		"group_id":     params.GroupID.String(),
		"party_id":     params.PartyID.String(),
		"version_lock": params.VersionLock.String(),
	}

	numericProps = map[string]float64{
		"rating_mu":    rating.Mu,
		"rating_sigma": rating.Sigma,
	}
	for i, v := range params.NumericProperties {
		numericProps[i] = v
	}

	for i, v := range params.StringProperties {
		stringProps[i] = v
	}
	qparts := []string{
		"+properties.mode:" + params.Mode.String(),
		"+properties.version_lock:" + params.VersionLock.String(),
		fmt.Sprintf("+properties.group_id:/(%s)/", Query.Escape(params.GroupID.String())),
		params.MatchmakingQueryAddon,
	}

	// Add a property of the external IP's RTT
	for ip, history := range params.latencyHistory {
		// Average the RTT
		rtt := 0

		if len(history) == 0 {
			continue
		}
		for _, v := range history {
			rtt += v
		}
		rtt /= len(history)
		if rtt == 0 {
			continue
		}

		k := ipToKey(net.ParseIP(ip))
		rtt = mroundRTT(rtt, 10)

		numericProps[k] = float64(rtt)
		if rtt > 100 {
			continue
		}

		qparts = append(qparts,
			fmt.Sprintf("properties.%s:<=%d", k, rtt+30),
		)
	}

	query = strings.Join(qparts, " ")

	return query, stringProps, numericProps, nil
}

// mroundRTT rounds the rtt to the nearest modulus
func mroundRTT[T time.Duration | int](rtt T, modulus T) T {
	if rtt == 0 {
		return 0
	}
	if rtt < modulus {
		return rtt
	}
	r := float64(rtt) / float64(modulus)
	return T(math.Round(r)) * modulus
}

// RTTweightedPopulationCmp compares two RTTs and populations
func RTTweightedPopulationCmp(i, j time.Duration, o, p int) bool {
	if i == 0 && j != 0 {
		return false
	}

	// Sort by if over or under 90ms
	if i < 90*time.Millisecond && j > 90*time.Millisecond {
		return true
	}
	if i > 90*time.Millisecond && j < 90*time.Millisecond {
		return false
	}

	// Sort by Population
	if o != p {
		return o > p
	}

	// If all else equal, sort by rtt
	return i < j
}

// PopulationCmp compares two populations
func PopulationCmp(i, j time.Duration, o, p int) bool {
	if o == p {
		// If all else equal, sort by rtt
		return i != 0 && i < j
	}
	return o > p
}

// LatencyCmp compares by latency, round to the nearest 10ms
func LatencyCmp[T int | time.Duration](i, j T, mround T) bool {
	// Round to the closest 10ms
	i = mroundRTT(i, mround)
	j = mroundRTT(j, mround)
	return i < j
}

type MatchmakingSettings struct {
	DisableArenaBackfill  bool     `json:"disable_arena_backfill,omitempty"` // Disable backfilling for arena matches
	BackfillQueryAddon    string   `json:"backfill_query_addon"`             // Additional query to add to the matchmaking query
	MatchmakingQueryAddon string   `json:"matchmaking_query_addon"`          // Additional query to add to the matchmaking query
	CreateQueryAddon      string   `json:"create_query_addon"`               // Additional query to add to the matchmaking query
	LobbyGroupID          string   `json:"group_id"`                         // Group ID to matchmake with
	PriorityBroadcasters  []string `json:"priority_broadcasters"`            // Prioritize these broadcasters
	NextMatchID           MatchID  `json:"next_match_id"`                    // Try to join this match immediately when finding a match
	NextMatchRole         string   `json:"next_match_role"`                  // The role to join the next match as
}

func (MatchmakingSettings) GetStorageID() StorageID {
	return StorageID{
		Collection: MatchmakingConfigStorageCollection,
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
