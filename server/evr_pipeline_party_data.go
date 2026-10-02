package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// SNS party data (proposal §3): the leader's party JSON and each member's own JSON, kept on the
// server so a member who joins later gets them, and relayed to nevr-runtime clients (social level
// >= 1) as SNSPartyDataNotify. The keys come from the game's scripts; the server fills the keys the
// game reads for matches and headsets (owner, 2026-10-01: filled by the server, nothing left blank).

const (
	snsPartyDataScopeParty  uint64 = 0
	snsPartyDataScopeMember uint64 = 1
	snsPartyDataMaxBytes           = 4096
)

// snsPartyDataEntry is one scope's latest JSON object and the writer's seq for it. Seq is per writing
// session (the client counts its own writes), so a write older than the stored one from the same
// session is dropped, and a new session (a new leader, a reconnected member) starts over.
type snsPartyDataEntry struct {
	session uuid.UUID
	seq     uint32
	data    map[string]any
}

// accepts reports whether a write (session, seq) is newer than the stored entry.
func (e *snsPartyDataEntry) accepts(session uuid.UUID, seq uint32) bool {
	return e == nil || e.session != session || seq > e.seq
}

// snsPartyDataState is one party's stored data: the party scope and one member scope per session.
type snsPartyDataState struct {
	sync.Mutex
	party   *snsPartyDataEntry
	members map[uuid.UUID]*snsPartyDataEntry // session id -> that member's data
}

func newSNSPartyDataState() *snsPartyDataState {
	return &snsPartyDataState{members: map[uuid.UUID]*snsPartyDataEntry{}}
}

// store keeps a write if it is newer than the stored one; it reports whether it was kept.
func (s *snsPartyDataState) store(scope uint64, session uuid.UUID, seq uint32, data map[string]any) bool {
	s.Lock()
	defer s.Unlock()
	entry := &snsPartyDataEntry{session: session, seq: seq, data: data}
	if scope == snsPartyDataScopeParty {
		if !s.party.accepts(session, seq) {
			return false
		}
		s.party = entry
		return true
	}
	if !s.members[session].accepts(session, seq) {
		return false
	}
	s.members[session] = entry
	return true
}

// snapshot copies a scope's script keys and seq (member: by session), empty when nothing was written.
func (s *snsPartyDataState) snapshot(scope uint64, session uuid.UUID) (map[string]any, uint32) {
	s.Lock()
	defer s.Unlock()
	entry := s.party
	if scope == snsPartyDataScopeMember {
		entry = s.members[session]
	}
	out := map[string]any{}
	if entry == nil {
		return out, 0
	}
	for k, v := range entry.data {
		out[k] = v
	}
	return out, entry.seq
}

// prune drops the member data of sessions no longer in the party (members leave by request, by
// disconnect or by joining elsewhere; the relay finds out the next time it lists the party).
func (s *snsPartyDataState) prune(present map[uuid.UUID]bool) {
	s.Lock()
	defer s.Unlock()
	for id := range s.members {
		if !present[id] {
			delete(s.members, id)
		}
	}
}

// parsePartyData accepts a JSON object of at most snsPartyDataMaxBytes.
func parsePartyData(raw []byte) (map[string]any, error) {
	if len(raw) > snsPartyDataMaxBytes {
		return nil, fmt.Errorf("party data is %d bytes, over %d", len(raw), snsPartyDataMaxBytes)
	}
	// The client's buffer may carry the C string's terminator.
	raw = []byte(strings.TrimRight(string(raw), "\x00"))
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("party data is not a JSON object: %w", err)
	}
	if data == nil {
		return nil, fmt.Errorf("party data is not a JSON object: null")
	}
	return data, nil
}

// Values the game reads when a user is in no match (pnsovr 0x1800ac430 stamped the same keys).
const (
	snsPartyNoLobbyID   = "00000000-0000-0000-0000-000000000000"
	snsPartyNoMatchType = int64(-1)
	snsPartyNoTeam      = 65535
)

// partyMatchKeys are the match keys for a user: the lobby they are in (upper-case GUID), its mode
// symbol, their team, and the lobby type; the no-match values when label is nil.
func partyMatchKeys(label *MatchLabel, userID uuid.UUID) map[string]any {
	keys := map[string]any{
		"lobbyid":   snsPartyNoLobbyID,
		"matchtype": snsPartyNoMatchType,
		"team":      snsPartyNoTeam,
		"lobbytype": int(UnassignedLobby),
	}
	if label == nil {
		return keys
	}
	keys["lobbyid"] = strings.ToUpper(label.ID.UUID.String())
	keys["matchtype"] = int64(label.Mode)
	keys["lobbytype"] = int(label.LobbyType)
	for _, player := range label.Players {
		if player.UserID == userID.String() {
			keys["team"] = int(player.Team)
			break
		}
	}
	return keys
}

// partyHeadsetType is the game's headset number (GetHeadsetTypeName 0x140170770: 1 Rift, 2 Rift S,
// 3 Quest, 4 Quest on PC (Link)), 0 for anything else (the game shows "Unknown"). A Quest headset on
// the PC build is on Link whether or not its name says so.
func partyHeadsetType(deviceType string, pcvr bool) int {
	switch {
	case strings.Contains(deviceType, "Rift S"):
		return 2
	case strings.Contains(deviceType, "Rift"):
		return 1
	case strings.Contains(deviceType, "Quest"):
		if pcvr || strings.Contains(deviceType, "(Link)") {
			return 4
		}
		return 3
	default:
		return 0
	}
}

// partyServerKeys are the keys the server fills for one user's data: the match keys and offline, plus
// headsettype for a member's data. They override whatever the client sent under the same names.
func (p *EvrPipeline) partyServerKeys(ctx context.Context, userID uuid.UUID, member bool) map[string]any {
	return partyServerKeysFor(p.userCurrentMatch(ctx, userID), p.userSessionParams(userID), userID, member)
}

// partyServerKeysFor builds the server's keys from the user's match (nil: none) and one of their live
// sessions' parameters (nil: no live session, so offline, and no headset known).
func partyServerKeysFor(label *MatchLabel, params *SessionParameters, userID uuid.UUID, member bool) map[string]any {
	keys := partyMatchKeys(label, userID)
	keys["offline"] = params == nil
	if member {
		headset := 0
		if params != nil {
			headset = partyHeadsetType(params.DeviceType(), params.IsPCVR())
		}
		keys["headsettype"] = headset
	}
	return keys
}

// partyDataJSON is the JSON a notify carries: the stored script keys with the server's keys over them.
func partyDataJSON(stored, serverKeys map[string]any) ([]byte, error) {
	for k, v := range serverKeys {
		stored[k] = v
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return nil, fmt.Errorf("party data encode: %w", err)
	}
	return raw, nil
}

// partyDataNotify builds one scope's notify: the stored script keys with the server keys over them.
// The party scope's server keys are the leader's.
func (p *EvrPipeline) partyDataNotify(ctx context.Context, state *snsPartyDataState, snsPartyID uint64, scope uint64,
	sessionID, userID uuid.UUID, memberID uint64) (*evr.SNSPartyDataNotify, error) {
	if scope == snsPartyDataScopeMember && memberID == 0 {
		// MemberID 0 means the party's data on the wire, so a member with no account id is not sent.
		return nil, fmt.Errorf("party data: no account id for member %s", userID)
	}
	data, seq := state.snapshot(scope, sessionID)
	raw, err := partyDataJSON(data, p.partyServerKeys(ctx, userID, scope == snsPartyDataScopeMember))
	if err != nil {
		return nil, err
	}
	if scope == snsPartyDataScopeParty {
		memberID = 0
	}
	return &evr.SNSPartyDataNotify{PartyID: snsPartyID, MemberID: memberID, Seq: seq, Json: raw}, nil
}

// partyDataState is the party's stored data, created on its first write.
func (p *EvrPipeline) partyDataState(partyUUID uuid.UUID) *snsPartyDataState {
	state, _ := p.snsPartyData.LoadOrStore(partyUUID, newSNSPartyDataState())
	return state
}

// partyDataStored is the party's stored data, or an empty state when nothing was written yet (the
// notifies then carry the server's keys alone).
func (p *EvrPipeline) partyDataStored(partyUUID uuid.UUID) *snsPartyDataState {
	if state, ok := p.snsPartyData.Load(partyUUID); ok {
		return state
	}
	return newSNSPartyDataState()
}

// snsPartyMember is one session in the party, as the data relay addresses it.
type snsPartyMember struct {
	session   Session
	userID    uuid.UUID
	accountID uint64
	level     int
}

// partyMembers lists the party's sessions on this node, and drops the stored data of sessions that
// have left. Account ids (a database lookup each) are resolved only when some member's client reads
// party data, so a party of stock clients costs nothing; readers reports whether one does.
func (p *EvrPipeline) partyMembers(ctx context.Context, logger *zap.Logger, partyUUID uuid.UUID) (members []snsPartyMember, readers bool) {
	stream := PresenceStream{Mode: StreamModeParty, Subject: partyUUID, Label: p.node}
	present := map[uuid.UUID]bool{}
	for _, presence := range p.nk.tracker.ListByStream(stream, true, true) {
		present[presence.ID.SessionID] = true
		session := p.nk.sessionRegistry.Get(presence.ID.SessionID)
		if session == nil {
			continue
		}
		m := snsPartyMember{session: session, userID: presence.UserID}
		if params, ok := LoadParams(session.Context()); ok {
			m.level = params.SocialLevel()
		}
		readers = readers || m.level >= 1
		members = append(members, m)
	}
	if readers {
		for i := range members {
			if accountID, err := p.resolveUserIDToAccountID(ctx, members[i].userID); err == nil {
				members[i].accountID = accountID
			}
		}
	}
	if len(present) == 0 {
		p.snsPartyEnded(logger, partyUUID) // nobody left: the party is over
	} else if state, ok := p.snsPartyData.Load(partyUUID); ok {
		state.prune(present)
	}
	return members, readers
}

// sendPartyData sends notifies to the members whose client reads them (social level >= 1), skipping
// the session `exclude`.
func sendPartyData(logger *zap.Logger, members []snsPartyMember, exclude uuid.UUID, notifies ...*evr.SNSPartyDataNotify) int {
	sent := 0
	msgs := make([]evr.Message, 0, len(notifies))
	for _, n := range notifies {
		msgs = append(msgs, n)
	}
	for _, m := range members {
		if m.level < 1 || m.session.ID() == exclude || len(msgs) == 0 {
			continue
		}
		if err := SendEVRMessages(m.session, false, msgs...); err != nil {
			logger.Warn("Failed to send party data", zap.String("to", m.userID.String()), zap.Error(err))
			continue
		}
		sent++
	}
	return sent
}

// snsPartyDataUpdateRequest stores a write of the party's data (leader only) or the sender's own member
// data, and relays it, with the server's keys filled, to the other members.
func (p *EvrPipeline) snsPartyDataUpdateRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	msg, ok := in.(*evr.SNSPartyDataUpdateRequest)
	if !ok {
		return fmt.Errorf("expected *evr.SNSPartyDataUpdateRequest, got %T", in)
	}
	failure := func(code uint8) error {
		if msg.TargetParam == snsPartyDataScopeMember {
			return SendEVRMessages(session, false, &evr.SNSPartyUpdateMemberFailure{ErrorCode: code})
		}
		return SendEVRMessages(session, false, &evr.SNSPartyUpdateFailure{ErrorCode: code})
	}
	params, ok := LoadParams(ctx)
	if !ok || params.currentPartyID == uuid.Nil {
		logger.Info("Party data refused: not in a party", zap.Uint64("scope", msg.TargetParam))
		return failure(1)
	}
	ph, ok := p.nk.partyRegistry.Get(params.currentPartyID)
	if !ok {
		logger.Info("Party data refused: party gone", zap.String("party", params.currentPartyID.String()))
		return failure(1)
	}
	if msg.TargetParam > snsPartyDataScopeMember {
		logger.Info("Party data refused: unknown scope", zap.Uint64("scope", msg.TargetParam))
		return failure(2)
	}
	if msg.TargetParam == snsPartyDataScopeParty {
		ph.RLock()
		isLeader := ph.leader != nil && ph.leader.UserPresence != nil && ph.leader.UserPresence.SessionId == session.ID().String()
		ph.RUnlock()
		if !isLeader {
			logger.Info("Party data refused: party scope from a non-leader", zap.String("party", params.currentPartyID.String()))
			return failure(2)
		}
	}
	data, err := parsePartyData(msg.Json)
	if err != nil {
		logger.Info("Party data refused", zap.Uint64("scope", msg.TargetParam), zap.Int("json_bytes", len(msg.Json)), zap.Error(err))
		return failure(3)
	}

	snsID := params.currentSNSPartyID
	accountID := p.sessionAccountID(ctx, session, params)
	if msg.TargetParam == snsPartyDataScopeMember && accountID == 0 {
		// MemberID 0 is the party's data on the wire: a member with no account id cannot be relayed.
		logger.Warn("Party data refused: no account id for the sender", zap.String("party", params.currentPartyID.String()))
		return failure(1)
	}
	state := p.partyDataState(params.currentPartyID)
	stored := state.store(msg.TargetParam, session.ID(), msg.Seq, data)
	members, _ := p.partyMembers(ctx, logger, params.currentPartyID)
	sent := 0
	if stored {
		notify, err := p.partyDataNotify(ctx, state, snsID, msg.TargetParam, session.ID(), session.UserID(), accountID)
		if err != nil {
			logger.Warn("Party data notify failed", zap.Error(err))
			return failure(3)
		}
		sent = sendPartyData(logger, members, session.ID(), notify)
	}
	logger.Info("Party data update", zap.String("party", params.currentPartyID.String()), zap.Uint64("sns_party_id", snsID),
		zap.Uint64("scope", msg.TargetParam), zap.Uint32("seq", msg.Seq), zap.Int("keys", len(data)),
		zap.Bool("stored", stored), zap.Int("relayed_to", sent))
	if msg.TargetParam == snsPartyDataScopeMember {
		return SendEVRMessages(session, false, &evr.SNSPartyUpdateMemberSuccess{PartyID: snsID})
	}
	return SendEVRMessages(session, false, &evr.SNSPartyUpdateSuccess{PartyID: snsID})
}

// snsPartyDataJoining sends the data a join needs before the join is announced, so a client that
// fires MemberJoined already holds the member's data (the game reads headsettype there,
// PartyMemberJoinedCB): the joiner gets the party's data and every other member's before its
// PartyJoinSuccess (its client keeps data for the party it is joining and adds every member it names,
// as pnsovr added a member when its data arrived), and the others get the joiner's before
// PartyJoinNotify. Called after the joiner is tracked in the party, before either message.
func (p *EvrPipeline) snsPartyDataJoining(ctx context.Context, logger *zap.Logger, session Session, partyUUID uuid.UUID, snsPartyID uint64) {
	members, readers := p.partyMembers(ctx, logger, partyUUID)
	ph, ok := p.nk.partyRegistry.Get(partyUUID)
	if !ok || !readers {
		return
	}
	state := p.partyDataStored(partyUUID)
	ph.RLock()
	var leaderSession, leaderUser uuid.UUID
	if ph.leader != nil && ph.leader.UserPresence != nil {
		leaderSession = uuid.FromStringOrNil(ph.leader.UserPresence.SessionId)
		leaderUser = uuid.FromStringOrNil(ph.leader.UserPresence.UserId)
	}
	ph.RUnlock()

	toJoiner := []*evr.SNSPartyDataNotify{}
	if leaderSession != uuid.Nil && leaderSession != session.ID() {
		if n, err := p.partyDataNotify(ctx, state, snsPartyID, snsPartyDataScopeParty, leaderSession, leaderUser, 0); err == nil {
			toJoiner = append(toJoiner, n)
		}
	}
	joiner := []snsPartyMember{}
	var own *evr.SNSPartyDataNotify
	for _, m := range members {
		n, err := p.partyDataNotify(ctx, state, snsPartyID, snsPartyDataScopeMember, m.session.ID(), m.userID, m.accountID)
		if err != nil {
			logger.Warn("Party data skipped a member", zap.Error(err))
			continue
		}
		if m.session.ID() == session.ID() {
			joiner = append(joiner, m)
			own = n
			continue
		}
		toJoiner = append(toJoiner, n)
	}
	sendPartyData(logger, joiner, uuid.Nil, toJoiner...)
	others := 0
	if own != nil {
		others = sendPartyData(logger, members, session.ID(), own)
	}
	logger.Info("Party data for a join", zap.String("party", partyUUID.String()), zap.Uint64("sns_party_id", snsPartyID),
		zap.Int("to_joiner", len(toJoiner)), zap.Bool("joiner_reads", len(joiner) == 1 && joiner[0].level >= 1),
		zap.Int("joiner_data_to", others))
}

// snsPartyDataMatchChanged re-sends a member's data (and the party's, if they lead it) to the whole
// party when they enter a match, so the match keys follow them.
func (p *EvrPipeline) snsPartyDataMatchChanged(ctx context.Context, logger *zap.Logger, session Session) {
	params, ok := LoadParams(session.Context())
	if !ok || params.currentPartyID == uuid.Nil {
		return
	}
	ph, ok := p.nk.partyRegistry.Get(params.currentPartyID)
	if !ok {
		return
	}
	members, readers := p.partyMembers(ctx, logger, params.currentPartyID)
	if !readers {
		return
	}
	state := p.partyDataStored(params.currentPartyID)
	notifies := []*evr.SNSPartyDataNotify{}
	ph.RLock()
	isLeader := ph.leader != nil && ph.leader.UserPresence != nil && ph.leader.UserPresence.SessionId == session.ID().String()
	ph.RUnlock()
	if isLeader {
		if n, err := p.partyDataNotify(ctx, state, params.currentSNSPartyID, snsPartyDataScopeParty, session.ID(), session.UserID(), 0); err == nil {
			notifies = append(notifies, n)
		}
	}
	var accountID uint64
	for _, m := range members {
		if m.session.ID() == session.ID() {
			accountID = m.accountID
		}
	}
	if n, err := p.partyDataNotify(ctx, state, params.currentSNSPartyID, snsPartyDataScopeMember, session.ID(), session.UserID(), accountID); err == nil {
		notifies = append(notifies, n)
	}
	sent := sendPartyData(logger, members, uuid.Nil, notifies...)
	logger.Info("Party data match change", zap.String("party", params.currentPartyID.String()),
		zap.Bool("leader", isLeader), zap.Int("notifies", len(notifies)), zap.Int("sent_to", sent))
}
