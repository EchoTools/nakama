package server

import (
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
)

type PartyGroup struct {
	name string
	ph   *PartyHandler
}

func (pg *PartyGroup) ID() uuid.UUID {
	pg.ph.RLock()
	defer pg.ph.RUnlock()
	return pg.ph.ID
}

func (pg *PartyGroup) GetLeader() *rtapi.UserPresence {
	p := pg.ph
	p.RLock()
	defer p.RUnlock()
	if p.leader == nil {
		return nil
	}
	return p.leader.UserPresence
}

func (pg *PartyGroup) GetMembers() []*PartyPresenceListItem {
	p := pg.ph
	p.RLock()
	defer p.RUnlock()
	return p.members.List()
}
