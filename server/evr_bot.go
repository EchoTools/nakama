package server

import "github.com/heroiclabs/nakama/v3/server/evr"

func IsBotEvrID(id evr.EvrId) bool {
	return id.PlatformCode == evr.BOT
}
