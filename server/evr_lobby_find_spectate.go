package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (p *EvrPipeline) lobbyFindSpectate(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters) error {

	limit := 100
	minSize := 1
	maxSize := MatchLobbyMaxSize - 1
	qparts := []string{
		"+label.open:T",
		"+label.lobby_type:public",
		fmt.Sprintf("+label.mode:%s", params.Mode.String()),
		fmt.Sprintf("+label.size:>=%d +label.size:<=%d", minSize, maxSize),
	}
	if params.Level != evr.LevelUnspecified {
		qparts = append(qparts, fmt.Sprintf("+label.level:%s", params.Level.String()))
	}

	query := strings.Join(qparts, " ")
	// create a delay timer
	listIntervalDelay := time.NewTimer(250 * time.Millisecond)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-listIntervalDelay.C:
		case <-time.After(3 * time.Second):
		}

		// list existing matches
		matches, err := listMatches(ctx, p.runtimeModule, limit, minSize+1, maxSize+1, query)
		if err != nil {
			return fmt.Errorf("failed to find spectate match: %w", err)
		}

		if len(matches) != 0 {

			// Shuffle the matches
			for i := range matches {
				j := rand.Intn(i + 1)
				matches[i], matches[j] = matches[j], matches[i]
			}

			slices.SortStableFunc(matches, func(a, b *api.Match) int {
				return int(a.Size - b.Size)
			})
			slices.Reverse(matches)

			for _, match := range matches {
				matchID := MatchIDFromStringOrNil(match.GetMatchId())
				if matchID.IsNil() {
					continue
				}
				label := MatchLabel{}
				if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), &label); err != nil {
					logger.Debug("Failed to parse match label", zap.Error(err))
					continue
				}
				// If there are already spectators in the match, skip it.
				if label.RoleCount(SpectatorRole) > 0 {
					continue
				}

				entrants, err := EntrantPresencesFromSessionIDs(logger, p.sessionRegistry, uuid.Nil, params.GroupID, nil, params.Role, session.ID())
				if err != nil {
					return status.Errorf(codes.Internal, "failed to create entrant presences: %v", err)
				}

				serverSession := p.sessionRegistry.Get(uuid.FromStringOrNil(label.Broadcaster.SessionID))
				if serverSession == nil {
					logger.Debug("Match broadcaster not found", zap.String("mid", match.GetMatchId()))
					continue
				}

				if err := p.LobbyJoinEntrant(logger, serverSession, &label, params.Role, entrants[0]); err != nil {
					// Send the error to the client
					if err := SendEVRMessages(session, LobbySessionFailureFromError(label.Mode, label.GetGroupID(), err)); err != nil {
						logger.Debug("Failed to send error message", zap.Error(err))
					}
				}
				return nil
			}
		}
	}
}
