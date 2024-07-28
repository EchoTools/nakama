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
	return g.ph.ID
}

func (g *PartyGroup) IDStr() string {
	return g.ph.IDStr
}
func (g *PartyGroup) GetLeader() *rtapi.UserPresence {
	g.ph.RLock()
	defer g.ph.RUnlock()
	if g.ph.leader == nil {
		return nil
	}
	return g.ph.leader.UserPresence
}

func (g *PartyGroup) List() []*PartyPresenceListItem {
	return g.ph.members.List()
}

func (g *PartyGroup) Size() int {
	return g.ph.members.Size()
}

func (g *PartyGroup) MatchmakerAdd(sessionID, node, query string, minCount, maxCount, countMultiple int, stringProperties map[string]string, numericProperties map[string]float64) (string, []*PresenceID, error) {
	return g.ph.MatchmakerAdd(sessionID, node, query, minCount, maxCount, countMultiple, stringProperties, numericProperties)
}
