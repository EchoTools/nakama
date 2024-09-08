package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

type TeamAlignments map[string]int // map[UserID]Role

func lobbyCreateSocial(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, session Session, matchRegistry MatchRegistry, params *LobbySessionParameters) (*MatchLabel, error) {

	qparts := []string{
		"+label.open:T",
		"+label.lobby_type:unassigned",
		fmt.Sprintf("+label.broadcaster.group_ids:/(%s)/", Query.Escape(params.GroupID.String())),
		fmt.Sprintf("+label.broadcaster.version_lock:%s", params.VersionLock),
	}

	query := strings.Join(qparts, " ")

	labels, err := lobbyListGameServers(ctx, logger, db, nk, session, query)
	if err != nil {
		logger.Warn("Failed to list game servers", zap.Any("query", query), zap.Error(err))
		return nil, err
	}

	// Pick a random label
	label := labels[rand.Intn(len(labels))]

	if err := lobbyPrepareSession(ctx, logger, matchRegistry, label.ID, params.Mode, params.Level, session.UserID(), params.GroupID, TeamAlignments{session.UserID().String(): params.Role}, time.Now().UTC()); err != nil {
		logger.Error("Failed to prepare session", zap.Error(err), zap.String("mid", label.ID.UUID.String()))
		return nil, err
	}

	match, _, err := matchRegistry.GetMatch(ctx, label.ID.String())
	if err != nil {
		return nil, errors.Join(NewLobbyErrorf(InternalError, "failed to get match"), err)
	} else if match == nil {
		logger.Warn("Match not found", zap.String("mid", label.ID.UUID.String()))
		return nil, ErrMatchNotFound
	}

	label = &MatchLabel{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
		return nil, errors.Join(NewLobbyError(InternalError, "failed to unmarshal match label"), err)
	}
	return label, nil

}

func lobbyPrepareSession(ctx context.Context, logger *zap.Logger, matchRegistry MatchRegistry, matchID MatchID, mode, level evr.Symbol, spawnedBy uuid.UUID, groupID uuid.UUID, teamAlignments TeamAlignments, startTime time.Time) error {
	label := &MatchLabel{
		ID:             matchID,
		Mode:           mode,
		Level:          level,
		SpawnedBy:      spawnedBy.String(),
		GroupID:        &groupID,
		TeamAlignments: teamAlignments,
		StartTime:      startTime,
	}
	response, err := SignalMatch(ctx, matchRegistry, matchID, SignalPrepareSession, label)
	if err != nil {
		return errors.Join(ErrMatchmakingUnknownError, fmt.Errorf("failed to prepare session `%s`: %s", label.ID.String(), response))
	}
	return nil
}
