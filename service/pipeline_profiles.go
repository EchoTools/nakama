package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

func (p *EvrPipeline) processUserServerProfileUpdate(ctx context.Context, logger *zap.Logger, evrID evr.XPID, label *MatchLabel, statistics *evr.ServerProfileUpdateStatistics) error {

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	// Get the player's information
	playerInfo := label.GetPlayerByEvrID(evrID)

	// If the player isn't in the match, or isn't a player, do not update the stats
	if playerInfo == nil || (playerInfo.Role != BlueTeam && playerInfo.Role != OrangeTeam) {
		return fmt.Errorf("non-player profile update request: %s", evrID.String())
	}
	logger = logger.With(zap.String("player_uid", playerInfo.UserID), zap.String("player_sid", playerInfo.SessionID), zap.String("player_xpid", playerInfo.XPID.String()))

	var profile *EVRProfile
	// Decrease the early quitter count for the player
	if playerSession := p.sessionRegistry.Get(uuid.FromStringOrNil(playerInfo.SessionID)); playerSession != nil {
		eqconfig := NewEarlyQuitConfig()
		if err := StorableReadNk(ctx, p.nk, playerInfo.UserID, eqconfig, true); err != nil {
			logger.Warn("Failed to load early quitter config", zap.Error(err))
		} else {
			eqconfig.IncrementCompletedMatches()
			if err := StorableWriteNk(ctx, p.nk, playerInfo.UserID, eqconfig); err != nil {
				logger.Warn("Failed to store early quitter config", zap.Error(err))
			} else if session := p.sessionRegistry.Get(playerSession.ID()); session != nil {
				if params, ok := LoadParams(session.Context()); ok {
					params.earlyQuitConfig.Store(eqconfig)
				}
			}
		}
	}

	var err error
	if profile == nil {
		// If the player isn't a member of the group, do not update the stats
		profile, err = EVRProfileLoad(ctx, p.nk, playerInfo.UserID)
		if err != nil {
			return fmt.Errorf("failed to get account profile: %w", err)
		}
	}
	groupIDStr := label.GetGroupID().String()

	if _, ok := profile.GetGroupDisplayName(groupIDStr); !ok {
		logger.Warn("Player is not a member of the group", zap.String("uid", playerInfo.UserID), zap.String("gid", groupIDStr))
		return nil
	}

	serviceSettings := ServiceSettings()

	validModes := []evr.Symbol{evr.ModeArenaPublic, evr.ModeCombatPublic}

	if serviceSettings.UseSkillBasedMatchmaking() && slices.Contains(validModes, label.Mode) {

		// Determine winning team
		blueWins := playerInfo.Role == BlueTeam && statistics.IsWinner()
		ratings := CalculateNewPlayerRatings(label.Players, blueWins)
		if rating, ok := ratings[playerInfo.SessionID]; ok {
			if err := MatchmakingRatingStore(ctx, p.nk, playerInfo.UserID, playerInfo.DiscordID, playerInfo.DisplayName, groupIDStr, label.Mode, rating); err != nil {
				logger.Warn("Failed to record percentile to leaderboard", zap.Error(err))
			}
		} else {
			logger.Warn("Failed to get player rating", zap.String("sessionID", playerInfo.SessionID))
		}
	}

	// Update the player's statistics, if the service settings allow it
	if serviceSettings.DisableStatisticsUpdates {
		return nil
	}

	var stats evr.Statistics

	// Select the correct statistics based on the mode
	switch label.Mode {
	case evr.ModeCombatPublic:
		if statistics.Combat == nil {
			return fmt.Errorf("missing combat statistics")
		}
		stats = statistics.Combat
	default:
		return fmt.Errorf("unknown mode: %s", label.Mode)
	}

	// Get the players existing statistics
	prevPlayerStats, _, err := PlayerStatisticsGetID(ctx, p.db, p.nk, playerInfo.UserID, label.GetGroupID().String(), []evr.Symbol{label.Mode}, label.Mode)
	if err != nil {
		return fmt.Errorf("failed to get player statistics: %w", err)
	}
	g := evr.StatisticsGroup{
		Mode:          label.Mode,
		ResetSchedule: evr.ResetScheduleAllTime,
	}

	// Use defaults if the player has no existing statistics
	prevStats, ok := prevPlayerStats[g]
	if !ok {
		prevStats = evr.NewServerProfile().Statistics[g]
	}

	entries, err := StatisticsToEntries(playerInfo.UserID, playerInfo.DisplayName, label.GetGroupID().String(), label.Mode, prevStats, stats)
	if err != nil {
		return fmt.Errorf("failed to convert statistics to entries: %w", err)
	}

	return WriteStatistics(ctx, p.nk, server.NewRuntimeGoLogger(logger), entries)
}

func StatisticsToEntries(userID, displayName, groupID string, mode evr.Symbol, prev, update evr.Statistics) ([]*StatisticsQueueEntry, error) {

	// Update the calculated fields
	if prev != nil {
		prev.CalculateFields()
	}
	update.CalculateFields()

	// Modify the update based on the previous stats
	updateElem := reflect.ValueOf(update).Elem()
	prevValue := reflect.ValueOf(prev)
	for i := range updateElem.NumField() {
		if field := updateElem.Field(i); !field.IsNil() && prevValue.IsValid() && !prevValue.IsNil() {
			stat := field.Interface().(*evr.StatisticValue)
			if stat == nil {
				continue
			}
			// If this is the XP field, just set it to the new value
			if stat.GetName() == "XP" {
				stat.SetValue(stat.GetValue())
			}
			// If the previous field exists, subtract the previous value from the current value
			prevField := prevValue.Elem().Field(i)
			if prevField.IsValid() && !prevField.IsNil() {
				if val := prevField.Interface().(*evr.StatisticValue); val != nil {
					stat.SetValue(stat.GetValue() - val.GetValue())
				}
			}
		}
	}

	resetSchedules := []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime}

	opMap := map[string]LeaderboardOperator{
		"avg": OperatorSet,
		"add": OperatorIncrement,
		"max": OperatorBest,
		"rep": OperatorSet,
	}

	// Create a map of stat names to their corresponding operator
	statsBaseType := reflect.ValueOf(evr.ArenaStatistics{}).Type()
	nameOperatorMap := make(map[string]LeaderboardOperator, statsBaseType.NumField())
	for i := range statsBaseType.NumField() {
		jsonTag := statsBaseType.Field(i).Tag.Get("json")
		statName := strings.SplitN(jsonTag, ",", 2)[0]
		opTag := statsBaseType.Field(i).Tag.Get("op")
		nameOperatorMap[statName] = opMap[opTag]
	}

	// construct the entries
	entries := make([]*StatisticsQueueEntry, 0, len(resetSchedules)*updateElem.NumField())
	for i := 0; i < updateElem.NumField(); i++ {
		updateField := updateElem.Field(i)

		for _, r := range resetSchedules {

			if updateField.IsNil() {
				continue
			}

			// Extract the JSON tag from the struct field
			jsonTag := updateElem.Type().Field(i).Tag.Get("json")
			statName := strings.SplitN(jsonTag, ",", 2)[0]

			meta := LeaderboardMeta{
				GroupID:       groupID,
				Mode:          mode,
				StatName:      statName,
				Operator:      nameOperatorMap[statName],
				ResetSchedule: r,
			}

			statValue := updateField.Interface().(*evr.StatisticValue).GetValue()

			// Skip stats that are not set or negative
			if statValue <= 0 {
				continue
			}

			score, subscore, err := Float64ToScore(statValue)
			if err != nil {
				return nil, fmt.Errorf("failed to convert float64 to int64 pair: %w", err)
			}

			entries = append(entries, &StatisticsQueueEntry{
				BoardMeta:   meta,
				UserID:      userID,
				DisplayName: displayName,
				Score:       score,
				Subscore:    subscore,
				Metadata:    nil,
			})
		}
	}

	return entries, nil
}

func (p *EvrPipeline) loggedInUserProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) (err error) {
	request := in.(*evr.LoggedInUserProfileRequest)
	// Start a timer to add to the metrics
	timer := time.Now()
	defer func() { p.nevr.MetricsTimerRecord("loggedInUserProfileRequest", nil, time.Since(timer)) }()

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}
	userID := session.userID.String()
	groupID := params.profile.GetActiveGroupID().String()

	modes := []evr.Symbol{
		evr.ModeArenaPublic,
		evr.ModeCombatPublic,
	}

	serverProfile, err := NewPlayerProfile(ctx, p.db, p.nk, params.profile, params.xpID, groupID, modes, 0, params.profile.GetGroupIGN(groupID))
	if err != nil {
		return fmt.Errorf("failed to create user server profile: %w", err)
	}

	clientProfile := NewClientProfile(ctx, params.profile, serverProfile)

	// Check if the user is required to go through community values
	journal := NewGuildEnforcementJournal(userID)
	if err := p.nevr.StorableRead(ctx, userID, journal, true); err != nil {
		logger.Warn("Failed to search for community values", zap.Error(err))
	} else if journal.CommunityValuesCompletedAt.IsZero() {
		clientProfile.Social.CommunityValuesVersion = 0
	}

	return session.SendEVR(Envelope{
		ServiceType: ServiceTypeLogin,
		Messages: []evr.Message{
			evr.NewLoggedInUserProfileSuccess(request.EvrID, clientProfile, serverProfile),
		},
	})
}

func (p *EvrPipeline) updateClientProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	request := in.(*evr.UpdateClientProfile)

	if err := p.handleClientProfileUpdate(ctx, logger, session, request.XPID, request.Payload); err != nil {
		if err := session.SendEVR(Envelope{
			ServiceType: ServiceTypeLogin,
			Messages: []evr.Message{
				evr.NewUpdateProfileFailure(request.XPID, uint64(500), err.Error()),
			},
			State: RequireStateUnrequired,
		}); err != nil {
			logger.Error("Failed to send UpdateProfileFailure", zap.Error(err))
		}
	}

	return session.SendEVR(Envelope{
		ServiceType: ServiceTypeLogin,
		Messages: []evr.Message{
			evr.NewUpdateProfileSuccess(&request.XPID),
		},
		State: RequireStateUnrequired,
	})
}

func (p *EvrPipeline) handleClientProfileUpdate(ctx context.Context, logger *zap.Logger, session *sessionEVR, evrID evr.XPID, update evr.ClientProfile) error {
	// Set the EVR ID from the context
	update.EvrID = evrID

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}
	userID := session.userID.String()
	groupID := params.profile.GetActiveGroupID().String()
	gg := p.guildGroupRegistry.Get(groupID)
	if gg == nil {
		return fmt.Errorf("guild group not found: %s", groupID)
	}

	hasCompleted := update.Social.CommunityValuesVersion != 0

	if hasCompleted {

		// Check if the user is required to go through community values
		journal := NewGuildEnforcementJournal(userID)
		if err := p.nevr.StorableRead(ctx, userID, journal, true); err != nil {
			logger.Warn("Failed to search for community values", zap.Error(err))
		} else if journal.CommunityValuesCompletedAt.IsZero() {

			journal.CommunityValuesCompletedAt = time.Now().UTC()

			if err := p.nevr.StorableWrite(ctx, userID, journal); err != nil {
				logger.Warn("Failed to write community values", zap.Error(err))
			}

			// Log the audit message
			if _, err := p.appBot.LogAuditMessage(ctx, groupID, fmt.Sprintf("User <@%s> (%s) has accepted the community values.", params.DiscordID(), params.profile.Username()), false); err != nil {
				logger.Warn("Failed to log audit message", zap.Error(err))
			}
		}

	}

	profile, err := EVRProfileLoad(ctx, p.nk, userID)
	if err != nil {
		return fmt.Errorf("failed to load account profile: %w", err)
	}

	profile.TeamName = update.TeamName

	profile.CombatLoadout = CombatLoadout{
		CombatWeapon:       update.CombatWeapon,
		CombatGrenade:      update.CombatGrenade,
		CombatDominantHand: update.CombatDominantHand,
		CombatAbility:      update.CombatAbility,
	}

	profile.LegalConsents = update.LegalConsents
	profile.GhostedPlayers = update.GhostedPlayers.Players
	profile.MutedPlayers = update.MutedPlayers.Players
	profile.NewUnlocks = update.NewUnlocks
	profile.CustomizationPOIs = update.Customization

	if err := EVRProfileUpdate(ctx, p.nk, userID, profile); err != nil {
		return fmt.Errorf("failed to update account profile: %w", err)
	}

	params.profile = profile
	StoreParams(ctx, &params)
	return nil
}

func WriteStatistics(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, entries []*StatisticsQueueEntry) error {
	for _, e := range entries {

		if e.Score == 0 && e.Subscore == 0 {
			continue
		}

		if !slices.Contains(ValidLeaderboardModes, e.BoardMeta.Mode) {
			continue
		}

		if _, err := nk.LeaderboardRecordWrite(ctx, e.BoardMeta.ID(), e.UserID, e.DisplayName, e.Score, e.Subscore, map[string]any{}, e.Override()); err != nil {

			// Try to create the leaderboard
			if err = nk.LeaderboardCreate(ctx, e.BoardMeta.ID(), true, "desc", string(e.BoardMeta.Operator), ResetScheduleToCron(e.BoardMeta.ResetSchedule), map[string]any{}, true); err != nil {

				logger.WithFields(map[string]any{
					"leaderboard_id": e.BoardMeta.ID(),
					"error":          err.Error(),
				}).Error("Failed to create leaderboard")
				return fmt.Errorf("failed to create leaderboard: %w", err)
			} else {
				logger.WithFields(map[string]any{
					"leaderboard_id": e.BoardMeta.ID(),
				}).Debug("Leaderboard created")

				if _, err := nk.LeaderboardRecordWrite(ctx, e.BoardMeta.ID(), e.UserID, e.DisplayName, e.Score, e.Subscore, map[string]any{}, e.Override()); err != nil {
					logger.WithFields(map[string]any{
						"error":          err.Error(),
						"leaderboard_id": e.BoardMeta.ID(),
						"user_id":        e.UserID,
						"score":          e.Score,
						"subscore":       e.Subscore,
					}).Error("Failed to write leaderboard record")
					return fmt.Errorf("failed to write leaderboard record: %w", err)
				}
			}
		}
	}
	return nil
}
