package server

import (
	"strings"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func IsBotEvrID(id evr.EvrId) bool {
	return id.PlatformCode == evr.BOT
}

func IsBotUserID(userID string) bool {
	return strings.HasPrefix(userID, "BOT-")
}
