package service

import (
	"context"
	"encoding/json"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

type NEVRNakamaModule struct {
	*server.RuntimeGoNakamaModule
	zapLogger *zap.Logger
}

func (n *NEVRNakamaModule) Logger() *zap.Logger {
	return n.zapLogger
}

// LobbyGet returns the MatchLabel for a given match ID
func (n *NEVRNakamaModule) LobbyGet(ctx context.Context, matchID string) (*MatchLabel, error) {
	match, err := n.MatchGet(ctx, matchID)
	if err != nil {
		return nil, err
	} else if match == nil {
		return nil, ErrMatchNotFound
	}

	label := MatchLabel{}
	if err = json.Unmarshal([]byte(match.GetLabel().GetValue()), &label); err != nil {
		return nil, err
	}
	if label.GroupID == nil {
		label.GroupID = &uuid.Nil
	}
	return &label, nil
}

// MatchLabelList returns a list of MatchLabels based on the provided parameters
func (n *NEVRNakamaModule) MatchLabelList(ctx context.Context, limit int, minSize, maxSize int, query string) ([]*MatchLabel, error) {
	var minSizePtr, maxSizePtr *int
	if minSize > 0 {
		minSizePtr = &minSize
	}
	if maxSize > 0 {
		maxSizePtr = &maxSize
	}
	match, err := n.MatchList(ctx, limit, true, "", minSizePtr, maxSizePtr, query)
	if err != nil {
		return nil, err
	} else if match == nil {
		return nil, ErrMatchNotFound
	}

	var labels []*MatchLabel

	for _, m := range match {
		label := MatchLabel{}
		if err = json.Unmarshal([]byte(m.GetLabel().GetValue()), &label); err != nil {
			return nil, err
		}
		if label.GroupID == nil {
			label.GroupID = &uuid.Nil
		}
		labels = append(labels, &label)
	}

	return labels, nil
}
