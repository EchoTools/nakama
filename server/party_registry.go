// Copyright 2021 The Nakama Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"errors"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"go.uber.org/zap"
)

var ErrPartyNotFound = errors.New("party not found")

type PartyRegistry interface {
	Create(open bool, maxSize int, leader *rtapi.UserPresence) *PartyHandler
	GetOrCreate(id uuid.UUID, open bool, maxSize int, leader *rtapi.UserPresence) (*PartyHandler, bool, error)
	GetOrCreateByGroupName(groupName string, open bool, maxSize int, leader *rtapi.UserPresence) (*PartyHandler, bool, error)
	LookupGroupPartyID(groupName string) (uuid.UUID, bool)
	Delete(id uuid.UUID)

	Join(id uuid.UUID, presences []*Presence)
	Leave(id uuid.UUID, presences []*Presence)

	PartyJoinRequest(ctx context.Context, id uuid.UUID, node string, presence *Presence) (bool, error)
	PartyPromote(ctx context.Context, id uuid.UUID, node, sessionID, fromNode string, presence *rtapi.UserPresence) error
	PartyAccept(ctx context.Context, id uuid.UUID, node, sessionID, fromNode string, presence *rtapi.UserPresence) error
	PartyRemove(ctx context.Context, id uuid.UUID, node, sessionID, fromNode string, presence *rtapi.UserPresence) error
	PartyClose(ctx context.Context, id uuid.UUID, node, sessionID, fromNode string) error
	PartyJoinRequestList(ctx context.Context, id uuid.UUID, node, sessionID, fromNode string) ([]*rtapi.UserPresence, error)
	PartyMatchmakerAdd(ctx context.Context, id uuid.UUID, node, sessionID, fromNode, query string, minCount, maxCount, countMultiple int, stringProperties map[string]string, numericProperties map[string]float64) (string, []*PresenceID, error)
	PartyMatchmakerRemove(ctx context.Context, id uuid.UUID, node, sessionID, fromNode, ticket string) error
	PartyDataSend(ctx context.Context, id uuid.UUID, node, sessionID, fromNode string, opCode int64, data []byte) error
	PartyList(ctx context.Context, limit int, open *bool, showHidden bool, query, cursor string) ([]*api.Party, string, error)
}

type LocalPartyRegistry struct {
	logger        *zap.Logger
	config        Config
	matchmaker    Matchmaker
	tracker       Tracker
	streamManager StreamManager
	router        MessageRouter
	node          string

	parties      *MapOf[uuid.UUID, *PartyHandler]
	groupParties *MapOf[string, uuid.UUID]
}

func NewLocalPartyRegistry(logger *zap.Logger, config Config, matchmaker Matchmaker, tracker Tracker, streamManager StreamManager, router MessageRouter, node string) PartyRegistry {
	return &LocalPartyRegistry{
		logger:        logger,
		config:        config,
		matchmaker:    matchmaker,
		tracker:       tracker,
		streamManager: streamManager,
		router:        router,
		node:          node,

		parties:      &MapOf[uuid.UUID, *PartyHandler]{},
		groupParties: &MapOf[string, uuid.UUID]{},
	}
}

func (p *LocalPartyRegistry) Create(open bool, maxSize int, presence *rtapi.UserPresence) *PartyHandler {
	id := uuid.Must(uuid.NewV4())
	partyHandler := NewPartyHandler(p.logger, p, p.matchmaker, p.tracker, p.streamManager, p.router, id, p.node, open, maxSize, presence)

	p.parties.Store(id, partyHandler)

	return partyHandler
}

func (p *LocalPartyRegistry) GetOrCreate(id uuid.UUID, open bool, maxSize int, leader *rtapi.UserPresence) (*PartyHandler, bool, error) {
	if id == uuid.Nil {
		return nil, false, errors.New("party ID must not be nil")
	}
	if maxSize <= 0 {
		return nil, false, errors.New("party maxSize must be greater than zero")
	}

	ph, found := p.parties.Load(id)
	if found {
		return ph, false, nil
	}
	ph = NewPartyHandler(p.logger, p, p.matchmaker, p.tracker, p.streamManager, p.router, id, p.node, open, maxSize, leader)
	actual, loaded := p.parties.LoadOrStore(id, ph)
	if loaded {
		// Another goroutine created it first, clean up ours.
		ph.ctxCancelFn()
		return actual, false, nil
	}
	return ph, true, nil
}

func (p *LocalPartyRegistry) GetOrCreateByGroupName(groupName string, open bool, maxSize int, leader *rtapi.UserPresence) (*PartyHandler, bool, error) {
	// Check if there's already a party for this group name.
	if existingID, ok := p.groupParties.Load(groupName); ok {
		return p.GetOrCreate(existingID, open, maxSize, leader)
	}

	newID := uuid.Must(uuid.NewV4())
	// Use LoadOrStore to avoid racing with another goroutine creating a party
	// for the same group name. If we lose the race, use the winner's ID.
	actualID, loaded := p.groupParties.LoadOrStore(groupName, newID)
	if loaded {
		newID = actualID
	}
	return p.GetOrCreate(newID, open, maxSize, leader)
}

func (p *LocalPartyRegistry) LookupGroupPartyID(groupName string) (uuid.UUID, bool) {
	return p.groupParties.Load(groupName)
}

func (p *LocalPartyRegistry) Delete(id uuid.UUID) {
	p.parties.Delete(id)
	// Remove any group name mapping that points to this party.
	p.groupParties.Range(func(name string, partyID uuid.UUID) bool {
		if partyID == id {
			p.groupParties.Delete(name)
		}
		return true
	})
}

func (p *LocalPartyRegistry) Join(id uuid.UUID, presences []*Presence) {
	ph, found := p.parties.Load(id)
	if !found {
		return
	}
	ph.Join(presences)
}

func (p *LocalPartyRegistry) Leave(id uuid.UUID, presences []*Presence) {
	ph, found := p.parties.Load(id)
	if !found {
		return
	}
	ph.Leave(presences)
}

func (p *LocalPartyRegistry) PartyJoinRequest(ctx context.Context, id uuid.UUID, node string, presence *Presence) (bool, error) {
	if node != p.node {
		return false, ErrPartyNotFound
	}

	ph, found := p.parties.Load(id)
	if !found {
		return false, ErrPartyNotFound
	}

	return ph.JoinRequest(presence)
}

func (p *LocalPartyRegistry) PartyPromote(ctx context.Context, id uuid.UUID, node, sessionID, fromNode string, presence *rtapi.UserPresence) error {
	if node != p.node {
		return ErrPartyNotFound
	}

	ph, found := p.parties.Load(id)
	if !found {
		return ErrPartyNotFound
	}

	return ph.Promote(sessionID, fromNode, presence)
}

func (p *LocalPartyRegistry) PartyAccept(ctx context.Context, id uuid.UUID, node, sessionID, fromNode string, presence *rtapi.UserPresence) error {
	if node != p.node {
		return ErrPartyNotFound
	}

	ph, found := p.parties.Load(id)
	if !found {
		return ErrPartyNotFound
	}

	return ph.Accept(sessionID, fromNode, presence, p.config.GetSession().SingleParty)
}

func (p *LocalPartyRegistry) PartyRemove(ctx context.Context, id uuid.UUID, node, sessionID, fromNode string, presence *rtapi.UserPresence) error {
	if node != p.node {
		return ErrPartyNotFound
	}

	ph, found := p.parties.Load(id)
	if !found {
		return ErrPartyNotFound
	}

	return ph.Remove(sessionID, fromNode, presence)
}

func (p *LocalPartyRegistry) PartyClose(ctx context.Context, id uuid.UUID, node, sessionID, fromNode string) error {
	if node != p.node {
		return ErrPartyNotFound
	}

	ph, found := p.parties.Load(id)
	if !found {
		return ErrPartyNotFound
	}

	return ph.Close(sessionID, fromNode)
}

func (p *LocalPartyRegistry) PartyJoinRequestList(ctx context.Context, id uuid.UUID, node, sessionID, fromNode string) ([]*rtapi.UserPresence, error) {
	if node != p.node {
		return nil, ErrPartyNotFound
	}

	ph, found := p.parties.Load(id)
	if !found {
		return nil, ErrPartyNotFound
	}

	return ph.JoinRequestList(sessionID, fromNode)
}

func (p *LocalPartyRegistry) PartyMatchmakerAdd(ctx context.Context, id uuid.UUID, node, sessionID, fromNode, query string, minCount, maxCount, countMultiple int, stringProperties map[string]string, numericProperties map[string]float64) (string, []*PresenceID, error) {
	if node != p.node {
		return "", nil, ErrPartyNotFound
	}

	ph, found := p.parties.Load(id)
	if !found {
		return "", nil, ErrPartyNotFound
	}

	return ph.MatchmakerAdd(sessionID, fromNode, query, minCount, maxCount, countMultiple, stringProperties, numericProperties)
}

func (p *LocalPartyRegistry) PartyMatchmakerRemove(ctx context.Context, id uuid.UUID, node, sessionID, fromNode, ticket string) error {
	if node != p.node {
		return ErrPartyNotFound
	}

	ph, found := p.parties.Load(id)
	if !found {
		return ErrPartyNotFound
	}

	return ph.MatchmakerRemove(sessionID, fromNode, ticket)
}

func (p *LocalPartyRegistry) PartyDataSend(ctx context.Context, id uuid.UUID, node, sessionID, fromNode string, opCode int64, data []byte) error {
	if node != p.node {
		return ErrPartyNotFound
	}

	ph, found := p.parties.Load(id)
	if !found {
		return ErrPartyNotFound
	}

	return ph.DataSend(sessionID, fromNode, opCode, data)
}

func (p *LocalPartyRegistry) PartyList(_ context.Context, limit int, open *bool, showHidden bool, _ string, _ string) ([]*api.Party, string, error) {
	results := make([]*api.Party, 0, limit)
	p.parties.Range(func(id uuid.UUID, ph *PartyHandler) bool {
		if open != nil && ph.Open != *open {
			return true
		}
		results = append(results, &api.Party{
			PartyId: ph.IDStr,
			Open:    ph.Open,
			MaxSize: int32(ph.MaxSize),
		})
		return len(results) < limit
	})
	return results, "", nil
}
