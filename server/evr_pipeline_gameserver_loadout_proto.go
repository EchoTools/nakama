package server

import (
	"context"
	"encoding/json"
	"fmt"

	rtapi "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/gameservice/v1"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// resolveGameServerLoadoutTarget is the gate for both loadout-save paths.
//
// A save is accepted only when the sender is the game server of a match, the
// entrant_id names a presence in that match, and the entrant's live session is
// also in that same match. The identity written is the presence's UserID, never
// an account id supplied by the sender.
func resolveGameServerLoadoutTarget(nk runtime.NakamaModule, senderID, entrantID uuid.UUID) (*EvrMatchPresence, MatchID, error) {
	senderMatch, _, err := GameServerBySessionID(nk, senderID)
	if err != nil {
		return nil, MatchID{}, fmt.Errorf("sender is not a game server: %w", err)
	}
	if entrantID.IsNil() {
		return nil, MatchID{}, fmt.Errorf("entrant id is empty")
	}
	presence, err := PresenceByEntrantID(nk, senderMatch, entrantID)
	if err != nil {
		return nil, MatchID{}, fmt.Errorf("entrant has no presence in the sender's match: %w", err)
	}
	if presence.UserID.IsNil() {
		return nil, MatchID{}, fmt.Errorf("entrant presence has no user id")
	}
	sessionMatch, _, err := GetMatchIDBySessionID(nk, presence.SessionID)
	if err != nil {
		return nil, MatchID{}, fmt.Errorf("entrant session has no live match: %w", err)
	}
	if sessionMatch.UUID != senderMatch.UUID {
		return nil, MatchID{}, fmt.Errorf("entrant session is in match %s, sender serves %s", sessionMatch.UUID, senderMatch.UUID)
	}
	return presence, senderMatch, nil
}

func applyGameServerSaveLoadoutProto(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, registry SessionRegistry, senderID uuid.UUID, msg *rtapi.GameServerSaveLoadoutMessage) error {
	if msg == nil {
		return nil
	}
	entrantID, err := uuid.FromString(msg.EntrantId)
	if err != nil || entrantID.IsNil() {
		logger.Warn("Rejecting save loadout with a non-UUID entrant_id", zap.String("entrant_id", msg.EntrantId))
		return nil
	}
	presence, matchID, err := resolveGameServerLoadoutTarget(nk, senderID, entrantID)
	if err != nil {
		logger.Warn("Rejecting save loadout", zap.String("entrant_id", msg.EntrantId), zap.Error(err))
		return nil
	}
	if msg.LobbySessionId != "" && msg.LobbySessionId != matchID.UUID.String() {
		logger.Warn("Rejecting save loadout with a mismatched lobby_session_id",
			zap.String("lobby_session_id", msg.LobbySessionId), zap.String("sender_match", matchID.UUID.String()))
		return nil
	}

	userID := presence.UserID.String()
	profile, err := EVRProfileLoad(ctx, nk, userID)
	if err != nil {
		logger.Warn("Failed to load EVR profile for save loadout", zap.String("user_id", userID), zap.Error(err))
		return nil
	}

	equips := make([]gameServerLoadoutEquip, 0, len(msg.LoadoutInstances))
	for _, instance := range msg.LoadoutInstances {
		for _, item := range instance.GetItems() {
			slotName := evr.ToSymbol(item.GetSlotType()).String()
			if !knownLoadoutSlot(slotName) {
				continue
			}
			equips = append(equips, gameServerLoadoutEquip{slot: slotName, equipped: evr.ToSymbol(item.GetEquippedItem()).String()})
		}
	}
	if _, err := persistGameServerLoadout(ctx, nk, userID, profile, equips, int(msg.JerseyNumber)); err != nil {
		logger.Warn("Failed to persist save loadout", zap.String("user_id", userID), zap.Error(err))
		return nil
	}
	refreshEntrantSessionProfile(ctx, nk, registry, presence)
	return nil
}

func applyGameServerSaveLoadoutLegacy(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, registry SessionRegistry, senderID uuid.UUID, request *evr.GameServerSaveLoadoutRequest) error {
	if request == nil {
		return nil
	}
	presence, _, err := resolveGameServerLoadoutTarget(nk, senderID, request.EntrantSessionID)
	if err != nil {
		logger.Warn("Rejecting legacy save loadout", zap.String("entrant_session_id", request.EntrantSessionID.String()), zap.Error(err))
		return nil
	}
	var payload GameServerLoadoutPayload
	if err := json.Unmarshal(request.Loadout, &payload); err != nil {
		logger.Warn("Failed to unmarshal legacy loadout payload", zap.Error(err))
		return nil
	}
	equips := make([]gameServerLoadoutEquip, 0, len(payload.LoadoutInstances))
	for _, instance := range payload.LoadoutInstances {
		for slotHex, equippedHex := range instance.Items {
			slotSymbol := evr.ToSymbol(slotHex)
			if slotSymbol == 0 {
				continue
			}
			slotName := slotSymbol.String()
			if !knownLoadoutSlot(slotName) {
				continue
			}
			equipped := evr.ToSymbol(equippedHex)
			if equipped == 0 {
				continue
			}
			equips = append(equips, gameServerLoadoutEquip{slot: slotName, equipped: equipped.String()})
		}
	}

	userID := presence.UserID.String()
	profile, err := EVRProfileLoad(ctx, nk, userID)
	if err != nil {
		logger.Warn("Failed to load EVR profile for legacy save loadout", zap.String("user_id", userID), zap.Error(err))
		return nil
	}
	if _, err := persistGameServerLoadout(ctx, nk, userID, profile, equips, payload.Number); err != nil {
		logger.Warn("Failed to persist legacy save loadout", zap.String("user_id", userID), zap.Error(err))
		return nil
	}
	refreshEntrantSessionProfile(ctx, nk, registry, presence)
	return nil
}

// refreshEntrantSessionProfile keeps the entrant's live session on the profile
// just written, the way the RemoteLogSet equip path does: a player who equips
// and then sees their session profile must not be looking at the old loadout.
func refreshEntrantSessionProfile(ctx context.Context, nk runtime.NakamaModule, registry SessionRegistry, presence *EvrMatchPresence) {
	if registry == nil || presence == nil {
		return
	}
	session := registry.Get(presence.SessionID)
	if session == nil {
		return
	}
	params, ok := LoadParams(session.Context())
	if !ok {
		return
	}
	updated, err := EVRProfileLoad(ctx, nk, presence.UserID.String())
	if err != nil {
		return
	}
	params.profile = updated
	StoreParams(session.Context(), params)
}

func (p *EvrPipeline) gameServerSaveLoadoutProtoHandler(logger *zap.Logger, session *sessionWS, in *rtapi.Envelope) error {
	return applyGameServerSaveLoadoutProto(session.Context(), logger, p.nk, p.sessionRegistry, session.ID(), in.GetGameServerSaveLoadout())
}
