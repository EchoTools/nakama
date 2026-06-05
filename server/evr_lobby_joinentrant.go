package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	rtapi "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/gameservice/v1"
	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

var (
	ErrFailedToTrackSessionID     = errors.New("failed to track session ID")
	ErrFailedToGetEntrantMetadata = errors.New("failed to get entrant metadata")
	ErrNoPresences                = errors.New("no presences")
	ErrServerSessionNotFound      = errors.New("server session not found")
	LobbyErrMatchNotFound         = NewLobbyError(ServerDoesNotExist, "match not found")
	LobbyErrMatchLabelEmpty       = NewLobbyError(ServerDoesNotExist, "match label empty")
	LobbyErrDuplicateEvrID        = NewLobbyError(BadRequest, "duplicate evr ID")
	LobbyErrMatchClosed           = NewLobbyError(ServerIsLocked, "match closed")
	LobbyErrJoinNotAllowed        = NewLobbyError(ServerIsFull, "join not allowed")
	ErrFailedToTrackEntrantStream = errors.New("failed to track entrant stream")
)

func (p *EvrPipeline) LobbyJoinEntrants(logger *zap.Logger, label *MatchLabel, presences ...*EvrMatchPresence) error {
	if len(presences) == 0 {
		return ErrNoPresences
	}

	session := p.nk.sessionRegistry.Get(presences[0].SessionID)
	if session == nil {
		return ErrSessionNotFound
	}

	if label.GameServer == nil {
		return fmt.Errorf("match has no game server")
	}
	serverSession := p.nk.sessionRegistry.Get(label.GameServer.SessionID)
	if serverSession == nil {
		return ErrServerSessionNotFound
	}

	// Validate pre-join ping for all entrants before allowing the join.
	if err := p.validatePreJoinPing(session.Context(), logger, label, presences); err != nil {
		return err
	}

	return LobbyJoinEntrants(logger, p.nk, p.nk.matchRegistry, p.nk.tracker, session, serverSession, label, presences...)
}
func LobbyJoinEntrants(logger *zap.Logger, nk *RuntimeGoNakamaModule, matchRegistry MatchRegistry, tracker Tracker, session Session, serverSession Session, label *MatchLabel, entrants ...*EvrMatchPresence) error {
	if session == nil || serverSession == nil {
		return ErrSessionNotFound
	}

	// ENFORCEMENT GATE (central chokepoint, all placement paths).
	//
	// lobbyAuthorize only runs on the three request-driven join paths and gates
	// against the REQUESTED group. Placement paths (matchmaker-built / party-
	// follow / backfill / spectator / setnextmatch) reach this function with a
	// BUILT match whose group may differ from the group the entrant authorized
	// into — or that ran NO authorize at all. Re-run the FULL per-guild
	// enforcement gate against the BUILT match's ACTUAL group+mode here, for
	// EVERY entrant, so request-path and chokepoint cannot diverge. Rejected
	// entrants are DROPPED instead of seated; if the primary entrant is rejected
	// the whole join is denied.
	//
	// This is per-guild ENFORCEMENT (suspension/membership/age/VPN/features),
	// NOT account-level DISABLED/ban (handled at login). Do not conflate them.
	entrants = enforceEntrantsAtChokepoint(logger, nk, label, entrants)
	if len(entrants) == 0 {
		// Every entrant was rejected by enforcement (or none were supplied).
		// Mirror the existing no-presences contract so callers surface a denial.
		return NewLobbyError(KickedFromLobbyGroup, "You are not permitted to join this guild.")
	}

	for _, e := range entrants {
		for _, feature := range label.RequiredFeatures {
			if !slices.Contains(e.SupportedFeatures, feature) {
				logger.With(zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String())).Warn("Player does not support required feature", zap.String("feature", feature), zap.String("mid", label.ID.UUID.String()), zap.String("uid", e.UserID.String()))
				return NewLobbyErrorf(MissingEntitlement, "player does not support required feature: %s", feature)
			}
		}
	}

	// Additional entrants are considered reservations
	metadata := EntrantMetadata{
		Presence:     entrants[0],
		Reservations: entrants[1:],
	}.ToMatchMetadata()

	e := entrants[0]

	sessionCtx := session.Context()

	var err error
	var found, allowed, isNew bool
	var reason string
	var labelStr string

	// Trigger MatchJoinAttempt
	found, allowed, isNew, reason, labelStr, _ = matchRegistry.JoinAttempt(sessionCtx, label.ID.UUID, label.ID.Node, e.UserID, e.SessionID, e.Username, e.SessionExpiry, nil, e.ClientIP, e.ClientPort, label.ID.Node, metadata)

	if reason == ErrJoinRejectReasonDuplicateJoin.Error() {
		logger.Debug("duplicate join attempt; no-op", zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String()), zap.String("mid", label.ID.UUID.String()))
		return nil
	}

	reservationViolated := reason == ErrJoinRejectReasonReservationViolated.Error()

	switch {
	case !found:
		err = LobbyErrMatchNotFound
	case labelStr == "":
		err = LobbyErrMatchLabelEmpty
	case reason == ErrJoinRejectDuplicateEvrID.Error():
		// Assuming ErrJoinRejectDuplicateEvrID is defined elsewhere and its Error() method returns the specific string
		err = LobbyErrDuplicateEvrID
	case reason == ErrJoinRejectReasonMatchClosed.Error():
		// Assuming ErrJoinRejectReasonMatchClosed is defined elsewhere and its Error() method returns the specific string
		err = LobbyErrMatchClosed
	case reservationViolated:
		// The lobby was genuinely over capacity despite the player holding a valid slot
		// reservation. This is a consistency violation — log at ERROR so it is visible.
		err = NewLobbyError(ServerIsFull, "lobby full: reservation violated — lobby was over capacity despite a valid slot reservation")
	case !allowed:
		// Wrap the base error with the specific reason provided by JoinAttempt
		err = fmt.Errorf("%w: %s", LobbyErrJoinNotAllowed, reason)
	}

	if err != nil {
		entrantUserIDs := make([]string, len(entrants))
		entrantSessionIDs := make([]string, len(entrants))
		for i, ent := range entrants {
			entrantUserIDs[i] = ent.UserID.String()
			entrantSessionIDs[i] = ent.SessionID.String()
		}

		groupID := ""
		if label.GroupID != nil {
			groupID = label.GroupID.String()
		}

		logFn := logger.Warn
		if reservationViolated {
			logFn = logger.Error
		}
		logFn("failed to join match",
			zap.Error(err),
			// Match info
			zap.String("mid", label.ID.UUID.String()),
			zap.String("mode", label.Mode.String()),
			zap.String("group_id", groupID),
			zap.Int("player_count", label.PlayerCount),
			zap.Int("player_limit", label.PlayerLimit),
			zap.Int("open_slots", label.OpenSlots()),
			zap.Bool("is_open", label.Open),
			// Request info
			zap.Int("party_size", len(entrants)),
			zap.Int("entrant_count", len(entrants)),
			zap.Strings("entrant_uids", entrantUserIDs),
			zap.Strings("entrant_sids", entrantSessionIDs),
			zap.Bool("reservation_violated", reservationViolated),
		)

		// Observer: join failed, player regrouping.
		if ws, ok := session.(*sessionWS); ok {
			if lc := getMatchLifecycle(ws); lc != nil {
				lc.Transition(StateSocialReady, "join failed, regrouping")
			}
		}

		return fmt.Errorf("failed to join match: %w", err)
	}

	if isNew {

		// The match handler will return an updated entrant presence with the generated entrant ID.
		e = &EvrMatchPresence{}
		if err := json.Unmarshal([]byte(reason), e); err != nil {
			return fmt.Errorf("failed to unmarshal match presence: %w", err)
		}
	}

	// Create the entrant stream using the entrant ID from the presence (generated by match handler)
	entrantStream := PresenceStream{Mode: StreamModeEntrant, Subject: e.EntrantID, Label: e.Node}

	if isNew {
		// Update the presence stream for the entrant.
		entrantMeta := PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: e.String(), Hidden: false}

		if success := tracker.Update(sessionCtx, e.SessionID, entrantStream, e.UserID, entrantMeta); !success {
			return ErrFailedToTrackEntrantStream
		}

	} else {

		// Use the existing entrant metadata.
		entrantMeta := tracker.GetLocalBySessionIDStreamUserID(e.SessionID, entrantStream, e.UserID)
		if entrantMeta == nil {
			// Presence was cleaned up (reconnect/disconnect race); fall back to treating as new.
			logger.Warn("entrant metadata not found, falling back to fresh metadata", zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String()))
			freshMeta := PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: e.String(), Hidden: false}
			if success := tracker.Update(sessionCtx, e.SessionID, entrantStream, e.UserID, freshMeta); !success {
				return ErrFailedToTrackEntrantStream
			}
		} else if err := json.Unmarshal([]byte(entrantMeta.Status), e); err != nil {
			return fmt.Errorf("failed to unmarshal entrant metadata: %w", err)
		}
	}

	// Wait before tracking, but respect context cancellation.
	select {
	case <-sessionCtx.Done():
		return fmt.Errorf("session closed before stream tracking: %w", sessionCtx.Err())
	case <-time.After(1 * time.Second):
	}

	matchIDStr := label.ID.String()

	guildGroupStream := PresenceStream{Mode: StreamModeGuildGroup, Subject: label.GetGroupID(), Label: label.Mode.String()}

	ops := []*TrackerOp{
		{
			guildGroupStream,
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: false},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: e.SessionID, Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: false},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: e.LoginSessionID, Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: false},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: e.UserID, Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: false},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: e.EvrID.UUID(), Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: false},
		},
	}

	// Update the statuses with retry logic to handle transient session state issues.
	// This is looked up by the pipeline when the game server sends the new entrant message,
	// and also by the IGP (In-Game Panel) to find players in matches.
	const maxRetries = 3
	const retryDelay = 100 * time.Millisecond

	for _, op := range ops {
		if ok := tracker.Update(sessionCtx, e.SessionID, op.Stream, e.UserID, op.Meta); !ok {
			logger.Warn("Failed to track stream", zap.Any("stream", op.Stream), zap.String("uid", e.UserID.String()))
			return ErrFailedToTrackSessionID
		}
	}

	// Leave any other lobby group stream.
	tracker.UntrackLocalByModes(session.ID(), map[uint8]struct{}{StreamModeMatchmaking: {}, StreamModeGuildGroup: {}}, guildGroupStream)

	connectionSettings := label.GetEntrantConnectMessage(e.RoleAlignment, e.DisableEncryption, e.DisableMAC)

	// Quest (standalone) clients use a different encoder flag bit layout (shifted by 1).
	if ServiceSettings().UseQuestEncoderFlags {
		if params, ok := LoadParams(sessionCtx); ok && !params.IsPCVR() {
			connectionSettings.UseQuestFlags = true
		}
	}

	// Create the protobuf envelope for the lobby session success message.
	successEnvelope := &rtapi.Envelope{
		Message: &rtapi.Envelope_LobbySessionSuccessV5{
			LobbySessionSuccessV5: &rtapi.SNSLobbySessionSuccessV5Message{
				GameMode:           uint64(connectionSettings.GameMode),
				LobbyId:            connectionSettings.LobbyID.String(),
				GroupId:            connectionSettings.GroupID.String(),
				Endpoint:           connectionSettings.Endpoint.String(),
				TeamIndex:          int32(connectionSettings.TeamIndex),
				SessionFlags:       uint32(connectionSettings.SessionFlags),
				ServerEncoderFlags: connectionSettings.ServerEncoderFlags.ToFlags(),
				ClientEncoderFlags: connectionSettings.ClientEncoderFlags.ToFlags(),
				ServerSequenceId:   connectionSettings.ServerSequenceId,
				ServerMacKey:       connectionSettings.ServerMacKey,
				ServerEncKey:       connectionSettings.ServerEncKey,
				ServerRandomKey:    connectionSettings.ServerRandomKey,
				ClientSequenceId:   connectionSettings.ClientSequenceId,
				ClientMacKey:       connectionSettings.ClientMacKey,
				ClientEncKey:       connectionSettings.ClientEncKey,
				ClientRandomKey:    connectionSettings.ClientRandomKey,
			},
		},
	}

	// Create the protobuf message wrapper.
	protobufMsg, err := evr.NewNEVRProtobufMessageV1(successEnvelope)
	if err != nil {
		logger.Error("failed to create protobuf message", zap.Error(err))
		return errors.New("failed to create protobuf message")
	}

	// Send the protobuf lobby session success message to the game server first.
	if err := SendEVRMessages(serverSession, false, protobufMsg, connectionSettings); err != nil {
		logger.Error("failed to send protobuf lobby session success to game server", zap.Error(err))
		return errors.New("failed to send protobuf lobby session success to game server")
	}

	// Observer: match found, sending LobbySessionSuccess to client.
	if ws, ok := session.(*sessionWS); ok {
		if lc := getMatchLifecycle(ws); lc != nil {
			if label.IsSocial() {
				lc.TransitionTo(StateSocialReady, "joined social lobby", WithMatchID(label.ID.String()))
			} else {
				lc.TransitionTo(StateJoining, "match found, joining", WithMatchID(label.ID.String()))
			}
		}
	}

	// Send the lobby session success message to the game client.
	<-time.After(150 * time.Millisecond)

	if err := SendEVRMessages(session, false, connectionSettings); err != nil {
		logger.Error("failed to send lobby session success to game client", zap.Error(err))
		return errors.New("failed to send lobby session success to game client")
	}

	// Clear matchmaking credits for all entrants (primary + reservations).
	// This ensures the next queue after a completed match starts fresh.
	for _, ent := range entrants {
		clearMatchmakingCredit(ent.UserID.String())
	}

	logger.Info("Joined entrant.", zap.String("mid", label.ID.UUID.String()), zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String()), zap.Int("role", e.RoleAlignment))
	return nil
}

// enforceEntrantsAtChokepoint runs the FULL per-guild enforcement gate against
// the BUILT match's actual group+mode for EVERY entrant and returns only the
// entrants that pass. Rejected entrants are dropped (logged) rather than seated.
//
// This is the central placement-path chokepoint. It uses a FRESH enforcement
// re-read (EnforcementJournalsLoad + CheckEnforcementSuspensions) so it catches
// suspensions issued AFTER the player's login snapshot (mid-session bans). If
// nk/registry deps are unavailable (e.g. a unit test constructing the module
// directly), it falls back to the login-time snapshot in session params so the
// suspension gate still holds.
func enforceEntrantsAtChokepoint(logger *zap.Logger, nk *RuntimeGoNakamaModule, label *MatchLabel, entrants []*EvrMatchPresence) []*EvrMatchPresence {
	if len(entrants) == 0 {
		return entrants
	}

	groupID := label.GetGroupID().String()
	mode := label.Mode

	var registry *GuildGroupRegistry
	if nk != nil {
		registry = nk.GuildGroupRegistry()
	}
	var gg *GuildGroup
	if registry != nil {
		gg = registry.Get(groupID)
	}

	kept := entrants[:0:0]
	for i, e := range entrants {
		ctx := entrantContext(nk, e)
		params, ok := LoadParams(ctx)
		if !ok || params == nil {
			// Without session params we cannot evaluate the enforcement gate.
			// Keep the entrant (fail open) but log — this mirrors the prior
			// behavior where the gate only ran when params were present.
			logger.Warn("Enforcement gate: no session params for entrant; allowing",
				zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String()),
				zap.String("mid", label.ID.UUID.String()), zap.String("group_id", groupID))
			kept = append(kept, e)
			continue
		}

		// Prefer a FRESH enforcement re-read (catches mid-session suspensions).
		// Fall back to the login-time snapshot if the re-read deps are missing
		// or the re-read fails (fail safe toward the snapshot, never fail open
		// on the suspension dimension).
		enforcements := params.gameModeSuspensionsByGroupID
		if nk != nil && registry != nil && len(params.enforcementUserIDs) > 0 {
			if journals, err := EnforcementJournalsLoad(ctx, nk, params.enforcementUserIDs); err != nil {
				logger.Warn("Enforcement gate: fresh journal re-read failed; using login snapshot",
					zap.String("uid", e.UserID.String()), zap.Error(err))
			} else if fresh, err := CheckEnforcementSuspensions(journals, registry.InheritanceByParentGroupID()); err != nil {
				logger.Warn("Enforcement gate: fresh suspension check failed; using login snapshot",
					zap.String("uid", e.UserID.String()), zap.Error(err))
			} else {
				enforcements = fresh
			}
		}

		decision := evaluateEntrantEnforcement(gg, params, enforcements, groupID, mode, e.UserID.String(), params.DiscordID())
		if decision.rejected {
			logger.Warn("Enforcement gate: rejected entrant from built match (placement path)",
				zap.String("mid", label.ID.UUID.String()),
				zap.String("uid", e.UserID.String()),
				zap.String("sid", e.SessionID.String()),
				zap.String("group_id", groupID),
				zap.String("mode", mode.String()),
				zap.String("reason", decision.metricTag),
				zap.Bool("is_primary", i == 0),
			)
			// Observer: rejected, player regrouping.
			if ws, ok := entrantSession(nk, e); ok {
				if lc := getMatchLifecycle(ws); lc != nil {
					lc.Transition(StateSocialReady, "join rejected by enforcement gate")
				}
			}
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

// asGoNakamaModule type-asserts a runtime.NakamaModule to the concrete
// *RuntimeGoNakamaModule used by the EVR pipeline. Returns nil if the module is
// not the Go module (e.g. in some unit tests); the chokepoint then falls back to
// the login-time enforcement snapshot rather than panicking.
func asGoNakamaModule(nk runtime.NakamaModule) *RuntimeGoNakamaModule {
	if gnk, ok := nk.(*RuntimeGoNakamaModule); ok {
		return gnk
	}
	return nil
}

// entrantContext returns the context carrying the entrant's session parameters.
// Falls back to context.Background() when the session cannot be resolved.
func entrantContext(nk *RuntimeGoNakamaModule, e *EvrMatchPresence) context.Context {
	if nk != nil && nk.sessionRegistry != nil {
		if s := nk.sessionRegistry.Get(e.SessionID); s != nil {
			return s.Context()
		}
	}
	return context.Background()
}

// entrantSession returns the entrant's *sessionWS for observer-lifecycle
// transitions, if resolvable.
func entrantSession(nk *RuntimeGoNakamaModule, e *EvrMatchPresence) (*sessionWS, bool) {
	if nk == nil || nk.sessionRegistry == nil {
		return nil, false
	}
	if s := nk.sessionRegistry.Get(e.SessionID); s != nil {
		if ws, ok := s.(*sessionWS); ok {
			return ws, true
		}
	}
	return nil, false
}

// entrantSuspensionRejection decides whether a user must be rejected from a
// match in groupID/mode based on the already-computed active enforcement map.
//
// This is the SINGLE source of truth for the per-guild SUSPENSION gate. It is
// shared by both the request-driven path (lobbyAuthorize, gating the REQUESTED
// group) and the placement path (LobbyJoinEntrants, gating the BUILT match's
// ACTUAL group). Keeping one function ensures the matchmaker-built / party-
// follow path cannot diverge from the request path and silently re-open the
// suspension bypass.
//
// This is SUSPENSION enforcement (per-guild GuildEnforcementJournal) only — it
// is NOT account-level DISABLED/ban enforcement, which is handled separately at
// login (authorizeSession). Do not conflate the two.
//
// enforcements is keyed map[groupID]map[mode]GuildEnforcementRecord (as produced
// by CheckEnforcementSuspensions), so expired/voided records are already absent.
// rejectSuspendedAlternates mirrors GuildGroup.RejectPlayersWithSuspendedAlternates;
// ignoreDisabledAlternates mirrors SessionParameters.ignoreDisabledAlternates.
//
// Returns the matched record and rejected=true when the join must be denied.
func entrantSuspensionRejection(enforcements ActiveGuildEnforcements, groupID string, mode evr.Symbol, userID string, rejectSuspendedAlternates, ignoreDisabledAlternates bool) (GuildEnforcementRecord, bool) {
	var suspensionRecord GuildEnforcementRecord

	recordsByGameMode := enforcements[groupID]
	if len(recordsByGameMode) == 0 {
		return suspensionRecord, false
	}

	for gameMode, r := range recordsByGameMode {
		if gameMode != mode {
			// Skip records for other game modes.
			continue
		}
		if r.IsExpired() || r.Expiry.Before(suspensionRecord.Expiry) {
			// Skip expired records.
			continue
		}
		if r.UserID != userID {
			// The suspension is for an alternate account.
			if ignoreDisabledAlternates {
				// User is excluded from suspension checks if they are ignoring disabled alternates.
				continue
			}
			if rejectSuspendedAlternates {
				suspensionRecord = r
			}
			// else: allowed (alternate tolerance); leave suspensionRecord unset.
		} else {
			suspensionRecord = r
		}
	}

	return suspensionRecord, !suspensionRecord.Expiry.IsZero()
}

// entrantEnforcementDecision is the result of the shared per-guild enforcement
// gate. rejected=true means the entrant must NOT be seated into the (built)
// match's group. metricTag/userReason carry the rejection classification.
type entrantEnforcementDecision struct {
	rejected   bool
	metricTag  string // stable key for metrics/logs (e.g. "suspended_user", "not_member")
	userReason string // message safe to surface to the rejected user
}

// evaluateEntrantEnforcement is the SINGLE source of truth for the FULL per-guild
// enforcement decision (the superset of what lobbyAuthorize checks): private-guild
// membership, role-based suspension, enforcement-journal suspension (incl.
// suspended-alternate handling and Public-Access-Denied), minimum account age,
// VPN/fraud-score blocking, and guild-allowed feature restrictions.
//
// It is called from BOTH the request-driven path (lobbyAuthorize, gating the
// REQUESTED group) and the placement/chokepoint path (LobbyJoinEntrants, gating
// the BUILT match's ACTUAL group+mode for every entrant). Keeping a single
// decision function ensures the matchmaker-built / party-follow / backfill /
// spectator / setnextmatch placement paths cannot diverge from the request path
// and silently re-open an enforcement bypass.
//
// This function is PURE DECISION ONLY — it performs NO Discord side-effects
// (audit messages, DMs, profile generation). Those remain in lobbyAuthorize on
// the request path. The chokepoint uses this as a defense-in-depth deny gate.
//
// This is per-guild ENFORCEMENT (GuildEnforcementJournal + guild roles/config) —
// it is NOT account-level DISABLED/ban enforcement, which is handled at login.
//
// freshEnforcements MUST be the (ideally freshly re-read) ActiveGuildEnforcements
// produced by CheckEnforcementSuspensions so expired/voided records are absent.
// discordID is used for the account-age snowflake check; pass "" to skip it.
func evaluateEntrantEnforcement(gg *GuildGroup, params *SessionParameters, freshEnforcements ActiveGuildEnforcements, groupID string, mode evr.Symbol, userID, discordID string) entrantEnforcementDecision {
	if gg == nil {
		// Without a guild group we cannot evaluate the guild-config dimensions
		// (membership, role-suspension, account-age, VPN, features). We STILL run
		// the journal-suspension dimension, which is group-keyed and does NOT
		// need the GuildGroup — this keeps the most critical (and most-evaded)
		// gate closed even if the built group is momentarily absent from the
		// registry. Alternates are treated leniently (rejectSuspendedAlternates
		// defaults false) since the guild's alt policy is unknown.
		if record, suspended := entrantSuspensionRejection(freshEnforcements, groupID, mode, userID, false, params.ignoreDisabledAlternates); suspended {
			reason := record.UserNoticeText
			if reason == "" {
				reason = "You are suspended from this guild."
			}
			metricTag := "suspended_user"
			if record.UserID != userID {
				metricTag = "suspended_alt_user"
			}
			return entrantEnforcementDecision{rejected: true, metricTag: metricTag, userReason: reason}
		}
		return entrantEnforcementDecision{}
	}

	// 1. Private-guild membership. (Discord API fallback is intentionally omitted
	//    here — it fails open on the request path too; this is defense in depth.)
	if gg.IsPrivate() && gg.RoleMap.Member != "" && !gg.IsMember(userID) {
		return entrantEnforcementDecision{rejected: true, metricTag: "not_member", userReason: "You are not a member of this private guild."}
	}

	// 2. Role-based suspension (suspended role / suspended XPID).
	if gg.IsSuspended(userID, &params.xpID) {
		return entrantEnforcementDecision{rejected: true, metricTag: "suspended_user", userReason: roleSuspensionUserMessage(gg.Name())}
	}

	// 3. Enforcement-journal suspension (incl. suspended-alternate + Public-Access-Denied).
	if record, suspended := entrantSuspensionRejection(freshEnforcements, groupID, mode, userID, gg.RejectPlayersWithSuspendedAlternates, params.ignoreDisabledAlternates); suspended {
		reason := record.UserNoticeText
		metricTag := "suspended_user"
		if record.UserID != userID {
			metricTag = "suspended_alt_user"
		}
		if record.SuspensionExcludesPrivateLobbies() {
			reason = "Public Access Denied: " + reason
			metricTag = "limited_access_user"
		}
		if reason == "" {
			reason = "You are suspended from this guild."
		}
		return entrantEnforcementDecision{rejected: true, metricTag: metricTag, userReason: reason}
	}

	// 4. Minimum account age.
	if gg.MinimumAccountAgeDays > 0 && discordID != "" && !gg.IsAccountAgeBypass(userID) {
		if t, err := discordgo.SnowflakeTimestamp(discordID); err == nil {
			if t.After(time.Now().AddDate(0, 0, -gg.MinimumAccountAgeDays)) {
				accountAge := int(time.Since(t).Hours() / 24)
				return entrantEnforcementDecision{rejected: true, metricTag: "account_age", userReason: fmt.Sprintf("Your account age (%d days) is too new (<%d days) to join this guild. ", accountAge, gg.MinimumAccountAgeDays)}
			}
		}
		// On snowflake parse error, fail open (matches request path returning an
		// error there; the request path already gated this entrant).
	}

	// 5. VPN / fraud-score blocking.
	if gg.BlockVPNUsers && params.isVPN && !gg.IsVPNBypass(userID) && params.ipInfo != nil {
		if params.ipInfo.FraudScore() >= gg.FraudScoreThreshold {
			guildName := gg.Name()
			return entrantEnforcementDecision{rejected: true, metricTag: "vpn_user", userReason: fmt.Sprintf("Disable VPN to join %s", guildName)}
		}
	}

	// 6. Guild-allowed feature restrictions.
	if len(gg.AllowedFeatures) > 0 {
		for _, feature := range params.supportedFeatures {
			if !slices.Contains(gg.AllowedFeatures, feature) {
				return entrantEnforcementDecision{rejected: true, metricTag: "feature_not_allowed", userReason: fmt.Sprintf("You are not allowed to join this guild with the feature `%s` enabled.", feature)}
			}
		}
	}

	return entrantEnforcementDecision{}
}

// lobbyAuthorize checks if the user is allowed to join the lobby based on various criteria such as guild membership, suspensions, and account age.
func (p *EvrPipeline) lobbyAuthorize(ctx context.Context, logger *zap.Logger, session Session, lobbyParams *LobbySessionParameters) error {
	groupID := lobbyParams.GroupID.String()
	metricsTags := map[string]string{
		"group_id": groupID,
	}

	defer func() {
		p.nk.MetricsCounterAdd("lobby_authorization", metricsTags, 1)
	}()

	logAuditMessage := func(message string) {
		if _, err := p.appBot.LogAuditMessage(ctx, groupID, message, true); err != nil {
			p.logger.Warn("Failed to send audit message", zap.String("channel_id", groupID), zap.Error(err))
		}
	}

	joinRejected := func(metricKey, reason, auditMessage string) error {
		metricsTags["error"] = metricKey
		auditMessage = fmt.Sprintf("Rejected lobby join by %s <@%s>[%s] to `%s`: %s", EscapeDiscordMarkdown(lobbyParams.DisplayName), lobbyParams.DiscordID, session.Username(), lobbyParams.Mode, auditMessage)
		if _, err := p.appBot.LogAuditMessage(ctx, groupID, auditMessage, true); err != nil {
			p.logger.Warn("Failed to send audit message", zap.String("channel_id", groupID), zap.Error(err))
		}
		return NewLobbyError(KickedFromLobbyGroup, reason)
	}

	userID := session.UserID().String()

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("failed to get session parameters")
	}

	gg := p.guildGroupRegistry.Get(groupID)
	if gg == nil {
		return fmt.Errorf("failed to get guild group: %s", groupID)
	}

	// Check if the user is a member of the (private) guild.
	if gg.IsPrivate() {
		if gg.RoleMap.Member != "" && !gg.IsMember(userID) {
			return joinRejected("not_member", "You are not a member of this private guild.", "not a member of private guild")
		} else {
			// If the member role is not set, the user must be a member of the guild.
			if guildMember, err := p.discordCache.GuildMember(gg.GuildID, lobbyParams.DiscordID); err != nil {
				if !IsDiscordErrorCode(err, discordgo.ErrCodeUnknownMember) {
					p.logger.Warn("Failed to get guild member. failing open.", zap.String("guild_id", gg.GuildID), zap.Error(err))
				}
			} else if guildMember == nil || guildMember.Pending {
				return joinRejected("not_member", "You are not a member of this private guild.", "not a member of private guild")
			}
		}
	}

	// User is suspended from the group.
	if gg.IsSuspended(userID, &params.xpID) {
		go func() {
			if p.discordCache.dg == nil {
				return
			}
			if _, err := SendUserMessage(context.Background(), p.discordCache.dg, lobbyParams.DiscordID, roleSuspensionDMMessage(gg.Name())); err != nil {
				p.logger.Warn("Failed to send suspension DM", zap.Error(err))
			}
		}()
		return joinRejected("suspended_user", roleSuspensionUserMessage(gg.Name()), roleSuspensionAuditMessage(gg.Name(), gg.RoleMap.Suspended))
	}

	// Re-read enforcement journals from storage to catch suspensions issued after login.
	freshJournals, freshJournalErr := EnforcementJournalsLoad(ctx, p.nk, params.enforcementUserIDs)
	if freshJournalErr != nil {
		p.logger.Error("failed to re-read enforcement journals at join time; denying join", zap.String("user_id", userID), zap.Error(freshJournalErr))
		return fmt.Errorf("failed to re-read enforcement journals: %w", freshJournalErr)
	}
	freshEnforcements, freshErr := CheckEnforcementSuspensions(freshJournals, p.guildGroupRegistry.InheritanceByParentGroupID())
	if freshErr != nil {
		p.logger.Error("failed to check fresh enforcement suspensions at join time; denying join", zap.String("user_id", userID), zap.Error(freshErr))
		return fmt.Errorf("failed to check enforcement suspensions: %w", freshErr)
	}

	if recordsByGameMode := freshEnforcements[groupID]; len(recordsByGameMode) > 0 {
		// Audit (only) the case where a suspended ALTERNATE is allowed through
		// because this guild tolerates alternates. The actual reject/allow
		// decision is made by the shared entrantSuspensionRejection gate below
		// so the request path and the placement path cannot diverge.
		if !gg.RejectPlayersWithSuspendedAlternates && !params.ignoreDisabledAlternates {
			for gameMode, r := range recordsByGameMode {
				if gameMode != lobbyParams.Mode || r.IsExpired() || r.UserID == userID {
					continue
				}
				logAuditMessage(fmt.Sprintf("Allowed alternate account <@!%s> (%s) of suspended user <@!%s> (%s): `%s` (expires <t:%d:R>)", lobbyParams.DiscordID, lobbyParams.DisplayName, r.UserID, session.Username(), r.UserNoticeText, r.Expiry.Unix()))
			}
		}

		suspensionRecord, suspended := entrantSuspensionRejection(freshEnforcements, groupID, lobbyParams.Mode, userID, gg.RejectPlayersWithSuspendedAlternates, params.ignoreDisabledAlternates)

		if suspended {
			const maxMessageLength = 64
			var metricTag, auditLog string

			reason := suspensionRecord.UserNoticeText

			if suspensionRecord.UserID == userID {
				// User has an active suspension
				auditLog = fmt.Sprintf("suspended (expires <t:%d:R>): `%s`", suspensionRecord.Expiry.Unix(), suspensionRecord.UserNoticeText)
				metricTag = "suspended_user"
			} else {
				// This is an alternate account of a suspended user.
				auditLog = fmt.Sprintf("suspended alternate (<@!%s>) (expires <t:%d:R>): `%s`", suspensionRecord.UserID, suspensionRecord.Expiry.Unix(), suspensionRecord.UserNoticeText)
				metricTag = "suspended_alt_user"
			}

			if suspensionRecord.SuspensionExcludesPrivateLobbies() {
				auditLog = fmt.Sprintf("Rejected limited access user (expires <t:%d:R>): `%s`", suspensionRecord.Expiry.Unix(), suspensionRecord.UserNoticeText)
				reason = "Public Access Denied: " + reason
				metricTag = "limited_access_user"
			}

			expires := fmt.Sprintf(" [exp: %s]", FormatDuration(time.Until(suspensionRecord.Expiry)))
			if len(reason)+len(expires) > maxMessageLength {
				reason = reason[:maxMessageLength-len(expires)-3] + "..."
			}

			reason = reason + expires
			return joinRejected(metricTag, reason, auditLog)
		}
	}

	if gg.MinimumAccountAgeDays > 0 && !gg.IsAccountAgeBypass(userID) {
		// Check the account creation date.
		t, err := discordgo.SnowflakeTimestamp(lobbyParams.DiscordID)
		if err != nil {
			return fmt.Errorf("failed to get discord snowflake timestamp: %w", err)
		}

		if t.After(time.Now().AddDate(0, 0, -gg.MinimumAccountAgeDays)) {
			accountAge := time.Since(t).Hours() / 24
			reason := fmt.Sprintf("Your account age (%d days) is too new (<%d days) to join this guild. ", int(accountAge), gg.MinimumAccountAgeDays)
			auditLog := fmt.Sprintf("account age (%d > %d days.", int(accountAge), gg.MinimumAccountAgeDays)
			return joinRejected("account_age", reason, auditLog)
		}
	}

	if gg.BlockVPNUsers && params.isVPN && !gg.IsVPNBypass(userID) && params.ipInfo != nil {
		if params.ipInfo.FraudScore() >= gg.FraudScoreThreshold {

			fields := []*discordgo.MessageEmbedField{
				{
					Name:   "Player",
					Value:  fmt.Sprintf("<@%s>", lobbyParams.DiscordID),
					Inline: false,
				},
				{
					Name:   "IP Address",
					Value:  session.ClientIP(),
					Inline: true,
				},
				{
					Name:   "Data Provider",
					Value:  params.ipInfo.DataProvider(),
					Inline: true,
				},
				{
					Name:   "Score",
					Value:  fmt.Sprintf("%d", params.ipInfo.FraudScore()),
					Inline: true,
				},
				{
					Name:   "ISP",
					Value:  params.ipInfo.ISP(),
					Inline: true,
				},
				{
					Name:   "Organization",
					Value:  params.ipInfo.Organization(),
					Inline: true,
				},
				{
					Name:   "ASN",
					Value:  fmt.Sprintf("%d", params.ipInfo.ASN()),
					Inline: true,
				},
				{
					Name:   "City",
					Value:  params.ipInfo.City(),
					Inline: true,
				},
				{
					Name:   "Region",
					Value:  params.ipInfo.Region(),
					Inline: true,
				},
				{
					Name:   "Country",
					Value:  params.ipInfo.CountryCode(),
					Inline: true,
				},
			}

			embed := &discordgo.MessageEmbed{
				Title:  "VPN User Rejected",
				Fields: fields,
				Color:  0xff0000, // Red color
			}
			message := &discordgo.MessageSend{
				Embeds: []*discordgo.MessageEmbed{embed},
			}
			if _, err := p.appBot.dg.ChannelMessageSendComplex(gg.AuditChannelID, message); err != nil {
				p.logger.Warn("Failed to send audit message", zap.String("channel_id", gg.AuditChannelID), zap.Error(err))
			}

			guildName := "This guild"
			if gg := params.guildGroups[groupID]; gg != nil {
				guildName = gg.Group.Name
			}
			reason := fmt.Sprintf("Disable VPN to join %s", guildName)
			auditLog := fmt.Sprintf("vpn probability score %d >= %d", params.ipInfo.FraudScore(), gg.FraudScoreThreshold)
			return joinRejected("vpn_user", reason, auditLog)
		}
	}

	if len(gg.AllowedFeatures) > 0 {
		allowedFeatures := gg.AllowedFeatures
		for _, feature := range params.supportedFeatures {
			if !slices.Contains(allowedFeatures, feature) {
				reason := fmt.Sprintf("You are not allowed to join this guild with the feature `%s` enabled.", feature)
				auditLog := fmt.Sprintf("feature `%s` not allowed in this guild", feature)
				return joinRejected("feature_not_allowed", reason, auditLog)
			}
		}
	}

	displayName := params.profile.GetGroupIGN(groupID)

	p.nk.Event(ctx, &api.Event{
		Name: EventLobbySessionAuthorized,
		Properties: map[string]string{
			"session_id":   session.ID().String(),
			"group_id":     groupID,
			"user_id":      userID,
			"discord_id":   params.DiscordID(),
			"display_name": displayName,
		},
		External: true, // used to denote if the event was generated from the client
	})

	// Force the players name to match in-game
	if gg.DisplayNameForceNickToIGN {
		go func() {
			// Search for them in a match from this guild
			member, err := p.discordCache.GuildMember(gg.GuildID, params.DiscordID())
			if err != nil {
				logger.Warn("Failed to get guild member", zap.Error(err))
			} else if member != nil {
				if displayName != InGameName(member) {
					AuditLogSendGuild(p.discordCache.dg, gg, fmt.Sprintf("Setting display name for `%s` to match in-game name: `%s`", EscapeDiscordMarkdown(member.User.Username), EscapeDiscordMarkdown(displayName)))
					// Force the display name to match the in-game name
					if err := p.discordCache.dg.GuildMemberNickname(gg.GuildID, member.User.ID, displayName); err != nil {
						logger.Warn("Failed to set display name", zap.Error(err))
					}
				}
			}
		}()
	}

	// Generate a profile for this group
	profile, err := UserServerProfileFromParameters(ctx, logger, p.db, p.nk, params, groupID, []evr.Symbol{lobbyParams.Mode}, lobbyParams.Mode)
	if err != nil {
		return fmt.Errorf("failed to create user server profile: %w", err)
	}

	if gg.IsEnforcer(userID) && params.isGoldNameTag.Load() && profile.DeveloperFeatures == nil {
		// Give the user a gold name if they are enabled as a moderator in the guild, and want it.
		profile.DeveloperFeatures = &evr.DeveloperFeatures{}
	}

	// Store the server profile for later retrieval by other players
	if err := ServerProfileStore(ctx, p.nk, userID, profile); err != nil {
		return fmt.Errorf("failed to store profile: %w", err)
	}

	session.Logger().Info("Authorized access to lobby session", zap.String("gid", groupID), zap.String("display_name", displayName))

	return nil
}

// roleSuspensionUserMessage returns the in-game message shown to a user suspended via guild role.
func roleSuspensionUserMessage(guildName string) string {
	return fmt.Sprintf("You are suspended from %s.", guildName)
}

// roleSuspensionAuditMessage returns the audit log message for a role-based suspension rejection.
func roleSuspensionAuditMessage(guildName, suspendedRoleID string) string {
	return fmt.Sprintf("is suspended from %s via role: <@&%s>", EscapeDiscordMarkdown(guildName), suspendedRoleID)
}

// roleSuspensionDMMessage returns the Discord DM content sent to a role-suspended user.
func roleSuspensionDMMessage(guildName string) string {
	return fmt.Sprintf("You have been suspended from **%s** via a server role. Contact a moderator of that server for more information.", EscapeDiscordMarkdown(guildName))
}
