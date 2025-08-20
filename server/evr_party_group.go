package server

import (
	"sync"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
)

type PartyGroup struct {
	sync.RWMutex
	name string
	ph   *PartyHandler
}

func (g *PartyGroup) ID() uuid.UUID {
	g.RLock()
	defer g.RUnlock()
	if g.ph == nil {
		return uuid.Nil
	}
	return g.ph.ID
}

func (g *PartyGroup) IDStr() string {
	g.RLock()
	defer g.RUnlock()
	if g.ph == nil {
		return uuid.Nil.String()
	}
	return g.ph.IDStr
}
func (g *PartyGroup) GetLeader() *rtapi.UserPresence {
	g.RLock()
	defer g.RUnlock()
	g.ph.RLock()
	defer g.ph.RUnlock()
	if g.ph.leader == nil {
		return nil
	}
	return g.ph.leader.UserPresence
}

func (g *PartyGroup) List() []*PartyPresenceListItem {
	g.RLock()
	defer g.RUnlock()

	if g.ph == nil {
		return nil
	}
	return g.ph.ListSorted()
}

func (g *PartyGroup) Size() int {
	g.RLock()
	defer g.RUnlock()
	if g.ph == nil {
		return 1
	}
	return g.ph.members.Size()
}

func (g *PartyGroup) MatchmakerAdd(sessionID, node, query string, minCount, maxCount, countMultiple int, stringProperties map[string]string, numericProperties map[string]float64) (string, []*PresenceID, error) {
	return g.ph.MatchmakerAdd(sessionID, node, query, minCount, maxCount, countMultiple, stringProperties, numericProperties)
}
