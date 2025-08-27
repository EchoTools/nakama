package server

import (
	"context"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// LobbyPartyConfiguration holds the result of party configuration
type LobbyPartyConfiguration struct {
	LobbyGroup        *LobbyGroup
	MemberSessionIDs  []uuid.UUID
	IsLeader          bool
	EntrantSessionIDs []uuid.UUID
}

// LobbyPartyConfigurator interface for testable party configuration
type LobbyPartyConfigurator interface {
	ConfigureParty(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters) (*LobbyPartyConfiguration, error)
}

// DefaultLobbyPartyConfigurator implements the LobbyPartyConfigurator interface
type DefaultLobbyPartyConfigurator struct {
	pipeline *EvrPipeline
}

// NewDefaultLobbyPartyConfigurator creates a new DefaultLobbyPartyConfigurator
func NewDefaultLobbyPartyConfigurator(pipeline *EvrPipeline) *DefaultLobbyPartyConfigurator {
	return &DefaultLobbyPartyConfigurator{pipeline: pipeline}
}

// ConfigureParty extracts and centralizes party configuration logic
func (c *DefaultLobbyPartyConfigurator) ConfigureParty(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters) (*LobbyPartyConfiguration, error) {
	entrantSessionIDs := []uuid.UUID{session.id}
	
	if lobbyParams.PartyGroupName == "" {
		lobbyParams.SetPartySize(1)
		return &LobbyPartyConfiguration{
			LobbyGroup:        nil,
			MemberSessionIDs:  []uuid.UUID{session.id},
			IsLeader:          true,
			EntrantSessionIDs: entrantSessionIDs,
		}, nil
	}

	// Join the party group
	lobbyGroup, memberSessionIDs, isLeader, err := c.pipeline.configureParty(ctx, logger, session, lobbyParams)
	if err != nil {
		return nil, fmt.Errorf("failed to join party: %w", err)
	}

	if !isLeader {
		// Skip following the party leader if the member is not in a match (and headed to a social lobby)
		if lobbyParams.Mode != evr.ModeSocialPublic || !lobbyParams.CurrentMatchID.IsNil() {
			return &LobbyPartyConfiguration{
				LobbyGroup:        lobbyGroup,
				MemberSessionIDs:  memberSessionIDs,
				IsLeader:          false,
				EntrantSessionIDs: nil, // Will be handled by PartyFollow
			}, nil
		}
	} else {
		// Add party members to entrant session IDs
		for _, memberSessionID := range memberSessionIDs {
			if memberSessionID == session.id {
				continue
			}
			entrantSessionIDs = append(entrantSessionIDs, memberSessionID)
		}
	}

	return &LobbyPartyConfiguration{
		LobbyGroup:        lobbyGroup,
		MemberSessionIDs:  memberSessionIDs,
		IsLeader:          isLeader,
		EntrantSessionIDs: entrantSessionIDs,
	}, nil
}

// EarlyQuitPenaltyHandler interface for testable early quit penalty logic
type EarlyQuitPenaltyHandler interface {
	ApplyPenalty(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters) error
}

// DefaultEarlyQuitPenaltyHandler implements the EarlyQuitPenaltyHandler interface  
type DefaultEarlyQuitPenaltyHandler struct {
	pipeline *EvrPipeline
}

// NewDefaultEarlyQuitPenaltyHandler creates a new DefaultEarlyQuitPenaltyHandler
func NewDefaultEarlyQuitPenaltyHandler(pipeline *EvrPipeline) *DefaultEarlyQuitPenaltyHandler {
	return &DefaultEarlyQuitPenaltyHandler{pipeline: pipeline}
}

// ApplyPenalty extracts and centralizes early quit penalty logic
func (h *DefaultEarlyQuitPenaltyHandler) ApplyPenalty(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters) error {
	// Only apply penalty for public arena matches with penalty enabled
	if lobbyParams.Mode != evr.ModeArenaPublic || lobbyParams.EarlyQuitPenaltyLevel <= 0 || !ServiceSettings().Matchmaking.EnableEarlyQuitPenalty {
		return nil
	}

	// Determine penalty interval based on level
	var interval time.Duration
	switch lobbyParams.EarlyQuitPenaltyLevel {
	case 1:
		interval = 60 * time.Second
	case 2:
		interval = 120 * time.Second
	case 3:
		interval = 240 * time.Second
	default:
		interval = 1 * time.Second
	}

	// Notify the user about the penalty
	message := fmt.Sprintf("Your early quit penalty is active (level %d), your matchmaking has been delayed by %d seconds.", lobbyParams.EarlyQuitPenaltyLevel, int(interval.Seconds()))
	if _, err := SendUserMessage(ctx, dg, lobbyParams.DiscordID, message); err != nil {
		logger.Warn("Failed to send message to user", zap.Error(err))
	}

	// Send audit log to guild
	if guildGroup := h.pipeline.guildGroupRegistry.Get(lobbyParams.GroupID.String()); guildGroup != nil {
		content := fmt.Sprintf("notified early quitter <@!%s> (%s): %s ", lobbyParams.DiscordID, session.Username(), message)
		if _, err := AuditLogSendGuild(h.pipeline.appBot.dg, guildGroup, content); err != nil {
			logger.Warn("Failed to send audit log message", zap.Error(err))
		}
	}

	// Apply the delay
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(interval):
		return nil
	}
}

// RefactoredLobbyFinder extracts the main logic with better testability
type RefactoredLobbyFinder struct {
	pipeline         *EvrPipeline
	authorizer       LobbyAuthorizer
	partyConfigurator LobbyPartyConfigurator
	penaltyHandler   EarlyQuitPenaltyHandler
}

// NewRefactoredLobbyFinder creates a new RefactoredLobbyFinder with default implementations
func NewRefactoredLobbyFinder(pipeline *EvrPipeline) *RefactoredLobbyFinder {
	return &RefactoredLobbyFinder{
		pipeline:         pipeline,
		authorizer:       NewDefaultLobbyAuthorizer(pipeline),
		partyConfigurator: NewDefaultLobbyPartyConfigurator(pipeline),
		penaltyHandler:   NewDefaultEarlyQuitPenaltyHandler(pipeline),
	}
}

// FindLobby is a refactored version of lobbyFind with better testability
func (f *RefactoredLobbyFinder) FindLobby(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters) error {
	startTime := time.Now()

	// Authorization checks (now centralized)
	authCtx := &LobbyAuthorizationContext{
		Session:     session,
		LobbyParams: lobbyParams,
		UserID:      session.UserID().String(),
		GroupID:     lobbyParams.GroupID.String(),
	}
	
	// Get additional context needed
	params, ok := LoadParams(ctx)
	if !ok {
		return fmt.Errorf("failed to get session parameters")
	}
	authCtx.Params = params
	
	gg := f.pipeline.guildGroupRegistry.Get(authCtx.GroupID)
	if gg == nil {
		return fmt.Errorf("failed to get guild group: %s", authCtx.GroupID)
	}
	authCtx.GuildGroup = gg

	result := f.authorizer.Authorize(ctx, logger, authCtx)
	if !result.Authorized {
		return NewLobbyError(KickedFromLobbyGroup, result.ErrorMessage)
	}

	// Mode validation
	if err := f.validateMode(lobbyParams); err != nil {
		return err
	}

	// Setup context with timeout
	ctx, cancel := context.WithTimeoutCause(ctx, lobbyParams.MatchmakingTimeout, ErrMatchmakingTimeout)
	defer cancel()

	// Join matchmaking stream
	if err := JoinMatchmakingStream(logger, session, lobbyParams); err != nil {
		return fmt.Errorf("failed to join matchmaking stream: %w", err)
	}

	// Monitor the matchmaking stream
	go f.pipeline.monitorMatchmakingStream(ctx, logger, session, lobbyParams, cancel)

	// Configure party (now extracted and testable)
	partyConfig, err := f.partyConfigurator.ConfigureParty(ctx, logger, session, lobbyParams)
	if err != nil {
		return err
	}

	// Handle non-leader party member case
	if !partyConfig.IsLeader && partyConfig.EntrantSessionIDs == nil {
		return f.pipeline.PartyFollow(ctx, logger, session, lobbyParams, partyConfig.LobbyGroup)
	}

	// Metrics
	f.pipeline.nk.metrics.CustomCounter("lobby_find_match", lobbyParams.MetricsTags(), int64(lobbyParams.GetPartySize()))
	logger.Info("Finding match", zap.String("mode", lobbyParams.Mode.String()), zap.Int("party_size", lobbyParams.GetPartySize()))

	// Prepare entrant presences
	entrants, err := PrepareEntrantPresences(ctx, logger, f.pipeline.nk, f.pipeline.nk.sessionRegistry, lobbyParams, partyConfig.EntrantSessionIDs...)
	if err != nil {
		return fmt.Errorf("failed to prepare entrant presences: %w", err)
	}

	lobbyParams.SetPartySize(len(entrants))

	// Set up metrics defer
	defer f.recordMetrics(startTime, partyConfig, lobbyParams, session, logger)

	// Check server ping
	if err := f.pipeline.CheckServerPing(ctx, logger, session, lobbyParams.GroupID.String()); err != nil {
		return fmt.Errorf("failed to check server ping: %w", err)
	}

	// Handle current match delay
	if !lobbyParams.CurrentMatchID.IsNil() {
		<-time.After(3 * time.Second)
	}

	// Apply early quit penalty (now extracted and testable)
	if err := f.penaltyHandler.ApplyPenalty(ctx, logger, session, lobbyParams); err != nil {
		return err
	}

	// Start matchmaking for competitive modes
	if f.shouldStartMatchmaking(lobbyParams.Mode) {
		go func() {
			if err := f.pipeline.lobbyMatchMakeWithFallback(ctx, logger, session, lobbyParams, partyConfig.LobbyGroup, entrants...); err != nil {
				logger.Error("Failed to matchmake", zap.Error(err))
			}
		}()
	}

	// Attempt to backfill
	enableFailsafe := true
	return f.pipeline.lobbyBackfill(ctx, logger, session, lobbyParams, enableFailsafe, entrants...)
}

// Helper methods extracted for clarity and testability

func (f *RefactoredLobbyFinder) validateMode(lobbyParams *LobbySessionParameters) error {
	switch lobbyParams.Mode {
	case evr.ModeArenaPublic, evr.ModeSocialPublic, evr.ModeCombatPublic:
		return nil
	default:
		return NewLobbyError(BadRequest, fmt.Sprintf("`%s` is an invalid mode for matchmaking.", lobbyParams.Mode.String()))
	}
}

func (f *RefactoredLobbyFinder) shouldStartMatchmaking(mode evr.Symbol) bool {
	return mode == evr.ModeArenaPublic || mode == evr.ModeCombatPublic
}

func (f *RefactoredLobbyFinder) recordMetrics(startTime time.Time, partyConfig *LobbyPartyConfiguration, lobbyParams *LobbySessionParameters, session *sessionWS, logger *zap.Logger) {
	isLeader := true
	if partyConfig.LobbyGroup != nil {
		leader := partyConfig.LobbyGroup.GetLeader()
		if leader != nil && leader.SessionId != session.id.String() {
			isLeader = false
		}
	}

	tags := lobbyParams.MetricsTags()
	tags["is_leader"] = fmt.Sprintf("%t", isLeader)
	tags["party_size"] = fmt.Sprintf("%d", lobbyParams.GetPartySize())
	f.pipeline.nk.metrics.CustomTimer("lobby_find_duration", tags, time.Since(startTime))

	logger.Debug("Lobby find complete", 
		zap.String("group_id", lobbyParams.GroupID.String()), 
		zap.Int("party_size", lobbyParams.GetPartySize()), 
		zap.String("mode", lobbyParams.Mode.String()), 
		zap.Int("role", lobbyParams.Role), 
		zap.Bool("leader", isLeader), 
		zap.Int("duration", int(time.Since(startTime).Seconds())))
}