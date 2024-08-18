package server

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (p *EvrPipeline) lobbyFindSpectate(ctx context.Context, logger *zap.Logger, session *sessionWS, params SessionParameters) error {

	limit := 100
	minSize := 1
	maxSize := MatchLobbyMaxSize - 1
	query := fmt.Sprintf("+label.open:T +label.lobby_type:public +label.mode:%s +label.size:>=%d +label.size:<=%d", params.Mode.Token(), minSize, maxSize)
	// creeate a delay timer
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

				entrants, err := EntrantPresencesFromSessionIDs(logger, p.sessionRegistry, uuid.Nil, params.GroupID, nil, params.Role, session.ID())
				if err != nil {
					return status.Errorf(codes.Internal, "failed to create entrant presences: %v", err)
				}

				if err := LobbyJoinEntrants(ctx, logger, p.matchRegistry, p.sessionRegistry, p.tracker, p.profileRegistry, matchID, params.Role, entrants); err != nil {
					logger.Debug("Failed to join match", zap.Error(err))
					continue
				}
				return nil
			}
		}
	}
}
