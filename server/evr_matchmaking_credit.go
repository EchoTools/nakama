package server

import (
	"sync"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

type matchmakingCredit struct {
	Mode      evr.Symbol
	Timestamp time.Time
	Expiry    time.Time
}

var matchmakingCredits sync.Map

func getMatchmakingCredit(userID string, mode evr.Symbol) *matchmakingCredit {
	val, ok := matchmakingCredits.Load(userID)
	if !ok {
		return nil
	}

	credit := val.(*matchmakingCredit)

	if time.Now().After(credit.Expiry) {
		matchmakingCredits.Delete(userID)
		return nil
	}

	if credit.Mode != mode {
		return nil
	}

	if credit.Timestamp.After(time.Now()) {
		matchmakingCredits.Delete(userID)
		return nil
	}

	return credit
}

func setMatchmakingCredit(userID string, credit *matchmakingCredit) {
	matchmakingCredits.Store(userID, credit)
}

func clearMatchmakingCredit(userID string) {
	matchmakingCredits.Delete(userID)
}

var matchmakingCreditModes = map[evr.Symbol]struct{}{
	evr.ModeArenaPublic:   {},
	evr.ModeCombatPublic:  {},
	evr.ModeArenaPublicAI: {},
}

func isMatchmakingCreditMode(mode evr.Symbol) bool {
	_, ok := matchmakingCreditModes[mode]
	return ok
}
