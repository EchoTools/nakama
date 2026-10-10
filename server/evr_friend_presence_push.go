package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// friendPresencePushInterval is the least time between presence pushes about one user: a lobby that
// churns (joins, leaves, lock, unlock) costs its members' friends one push per window, not one per event.
const friendPresencePushInterval = 2 * time.Second

// friendPresenceSweepPeriods is how many intervals pass between sweeps of the limiter's idle windows.
const friendPresenceSweepPeriods = 4

// friendPresenceLimiter spaces the pushes about each user: the first change goes at once, the changes
// inside the window after it are folded into one trailing push at the window's end. The zero value works.
type friendPresenceLimiter struct {
	interval time.Duration
	now      func() time.Time

	mu        sync.Mutex
	state     map[uuid.UUID]*friendPresenceWindow
	nextSweep time.Time // the next time the idle windows are dropped
}

type friendPresenceWindow struct {
	last    time.Time
	pending bool // the trailing push is scheduled
}

func (l *friendPresenceLimiter) clock() (time.Time, time.Duration) {
	interval := l.interval
	if interval <= 0 {
		interval = friendPresencePushInterval
	}
	if l.now != nil {
		return l.now(), interval
	}
	return time.Now(), interval
}

// admit reports whether a push about the user may go now; if not, schedule is how long until the one
// trailing push that carries every change made meanwhile (0 when that push is already scheduled).
func (l *friendPresenceLimiter) admit(user uuid.UUID) (now bool, schedule time.Duration) {
	t, interval := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == nil {
		l.state = map[uuid.UUID]*friendPresenceWindow{}
	}
	if !t.Before(l.nextSweep) {
		// Forget the users whose window has passed. One scan per sweep period, whatever the number of
		// admits, so the cost per admit is constant and the map holds at most the users seen in about
		// two periods.
		for id, w := range l.state {
			if !w.pending && t.Sub(w.last) >= interval {
				delete(l.state, id)
			}
		}
		l.nextSweep = t.Add(friendPresenceSweepPeriods * interval)
	}
	w := l.state[user]
	switch {
	case w == nil:
		l.state[user] = &friendPresenceWindow{last: t}
		return true, 0
	case w.pending:
		return false, 0
	case t.Sub(w.last) >= interval:
		w.last = t
		return true, 0
	default:
		w.pending = true
		return false, w.last.Add(interval).Sub(t)
	}
}

// fired records that the trailing push for the user has run: it counts as a push, so the window restarts.
func (l *friendPresenceLimiter) fired(user uuid.UUID) {
	t, _ := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := l.state[user]; w != nil {
		w.pending = false
		w.last = t
	}
}

// friendPresencePush is the pipeline's state for pushing presence; its zero value works, and the
// function fields are the seams the tests replace.
type friendPresencePush struct {
	limiter friendPresenceLimiter
	// friends is the user's confirmed friends who are online; nil reads the database.
	friends func(ctx context.Context, subject uuid.UUID) ([]uuid.UUID, error)
	// accountID is the id the clients know the user by; nil resolves it as the friend list does.
	accountID func(ctx context.Context, subject uuid.UUID) (uint64, error)
	// schedule runs f after delay; nil runs it on its own goroutine (delay 0) or a timer.
	schedule func(delay time.Duration, f func())
}

func (f *friendPresencePush) run(delay time.Duration, fn func()) {
	switch {
	case f.schedule != nil:
		f.schedule(delay, fn)
	case delay <= 0:
		go fn()
	default:
		time.AfterFunc(delay, fn)
	}
}

// onlineFriendIDs is the user's confirmed friends who are online now.
func (p *EvrPipeline) onlineFriendIDs(ctx context.Context, logger *zap.Logger, subject uuid.UUID) ([]uuid.UUID, error) {
	if p.friendPush.friends != nil {
		return p.friendPush.friends(ctx, subject)
	}
	if p.db == nil {
		return nil, fmt.Errorf("friend list for user %s: no database", subject)
	}
	friends, err := ListPlayerFriends(ctx, logger, p.db, p.nk.statusRegistry, subject)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(friends))
	for _, f := range friends {
		if f == nil || f.State == nil || f.State.Value != FriendStateFriends || f.User == nil || !f.User.Online {
			continue
		}
		if id := uuid.FromStringOrNil(f.User.Id); id != uuid.Nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// friendPresenceChanged tells the user's online friends where the user is now, rate limited per user:
// at once for the first change, and one trailing push for the changes that follow inside the window.
func (p *EvrPipeline) friendPresenceChanged(ctx context.Context, logger *zap.Logger, userID uuid.UUID, trigger string) {
	if userID == uuid.Nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	now, delay := p.friendPush.limiter.admit(userID)
	switch {
	case now:
		p.friendPush.run(0, func() { p.pushFriendPresence(ctx, logger, userID, trigger, false) })
	case delay > 0:
		p.friendPush.run(delay, func() {
			p.friendPush.limiter.fired(userID)
			p.pushFriendPresence(ctx, logger, userID, trigger, true)
		})
	}
}

// friendPresencePartyChanged pushes presence about every member of the party and about the user who
// caused the change (who may no longer be a member): the party's size, lock, policy and leader decide
// what each member's friends are offered.
func (p *EvrPipeline) friendPresencePartyChanged(ctx context.Context, logger *zap.Logger, userID uuid.UUID, partyUUID uuid.UUID, trigger string) {
	users := map[uuid.UUID]struct{}{userID: {}}
	if partyUUID != uuid.Nil {
		stream := PresenceStream{Mode: StreamModeParty, Subject: partyUUID, Label: p.node}
		for _, presence := range p.nk.tracker.ListByStream(stream, true, true) {
			users[presence.UserID] = struct{}{}
		}
	}
	for id := range users {
		p.friendPresenceChanged(ctx, logger, id, trigger)
	}
}

// friendPresenceMatchEntered is what runs when a session is admitted to a match.
func (p *EvrPipeline) friendPresenceMatchEntered(ctx context.Context, logger *zap.Logger, session Session) {
	p.friendPresenceChanged(ctx, logger, session.UserID(), "match_entered")
}

// friendPresenceOfflineGrace is how long a closed session's user is given to have another session
// again before their friends are told they are offline, so a reconnect does not show as a flash offline.
const friendPresenceOfflineGrace = friendPresencePushInterval

// friendPresenceOnline is what runs when a session logs in: the user's friends are told they are online.
func (p *EvrPipeline) friendPresenceOnline(ctx context.Context, logger *zap.Logger, userID uuid.UUID) {
	p.friendPresenceChanged(ctx, logger, userID, friendTriggerOnline)
}

// friendPresenceOffline is what runs when a session closes. It never blocks the close: the push, with
// its friend listing and database lookups, runs on a timer after the grace period and reads whether the
// user is online then.
func (p *EvrPipeline) friendPresenceOffline(ctx context.Context, logger *zap.Logger, userID uuid.UUID) {
	if userID == uuid.Nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	p.friendPush.run(friendPresenceOfflineGrace, func() {
		p.friendPresenceChanged(ctx, logger, userID, friendTriggerOffline)
	})
}

// The triggers that are a change of the user's online state: their friends are sent the status
// notification (the roster entry) as well as the presence.
const (
	friendTriggerOnline  = "online"
	friendTriggerOffline = "offline"
)

// pushFriendPresence tells each online friend of the subject, on every live session of theirs, where the
// subject is. A session whose client parses presence (social level >= 1, as sendFriendPresence gates)
// is sent one SNSFriendPresenceNotify built for that viewer. On a change of online state, and for a
// subject who has gone offline, every session is also sent the SNSFriendStatusNotify the friend list
// response sends all clients (it is what puts the friend in the roster, so stock clients parse it).
func (p *EvrPipeline) pushFriendPresence(ctx context.Context, logger *zap.Logger, subject uuid.UUID, trigger string, trailing bool) {
	start := time.Now()
	fields := []zap.Field{zap.String("subject", subject.String()), zap.String("trigger", trigger), zap.Bool("trailing", trailing)}
	// The state is read now, not when the change happened: a user with no live session is offline, and
	// a push that waited out its window reports where they are at the end of it.
	online := len(p.nk.tracker.ListByStream(PresenceStream{Mode: StreamModeStatus, Subject: subject}, true, true)) > 0
	fields = append(fields, zap.Bool("online", online))
	sendStatus := !online || trigger == friendTriggerOnline || trigger == friendTriggerOffline
	friends, err := p.onlineFriendIDs(ctx, logger, subject)
	if err != nil {
		logger.Warn("Friend presence push: could not list friends", append(fields, zap.Error(err))...)
		return
	}
	if len(friends) == 0 {
		logger.Debug("Friend presence push: no online friends", fields...)
		return
	}
	var accountID uint64
	if p.friendPush.accountID != nil {
		accountID, err = p.friendPush.accountID(ctx, subject)
	} else {
		accountID, err = p.resolveUserIDToAccountID(ctx, subject)
	}
	if err != nil || accountID == 0 {
		logger.Warn("Friend presence push: no account id for the subject", append(fields, zap.Error(err))...)
		return
	}
	sent, viewers := 0, 0
	for _, friend := range friends {
		for _, presence := range p.nk.tracker.ListByStream(PresenceStream{Mode: StreamModeStatus, Subject: friend}, true, true) {
			ws, ok := p.nk.sessionRegistry.Get(presence.ID.SessionID).(*sessionWS)
			if !ok || ws == nil {
				continue
			}
			params, ok := LoadParams(ws.Context())
			if !ok {
				continue
			}
			viewers++
			var messages []evr.Message
			if sendStatus {
				messages = append(messages, &evr.SNSFriendStatusNotify{FriendID: accountID, StatusCode: friendStatusCode(online)})
			}
			if params.SocialLevel() >= 1 {
				messages = append(messages, p.friendPresence(ctx, friend, subject, accountID, online))
			}
			if len(messages) == 0 {
				continue
			}
			if err := SendEVRMessages(ws, false, messages...); err != nil {
				logger.Warn("Friend presence push: send failed", append(fields, zap.String("viewer", friend.String()), zap.Error(err))...)
				continue
			}
			sent++
		}
	}
	logger.Info("Friend presence pushed", append(fields, zap.Uint64("account_id", accountID), zap.Int("friends", len(friends)),
		zap.Int("viewers", viewers), zap.Int("sent", sent), zap.Duration("latency", time.Since(start)))...)
}
