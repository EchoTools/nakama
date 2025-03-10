package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.mongodb.org/mongo-driver/mongo"
)

const (
	EventLobbySessionAuthorized = "lobby_session_authorized"
	EventUserLogin              = "user_login"
	EventAccountUpdated         = "account_updated"
	EventSessionStart           = "session_start"
	EventVRMLAccountLinked      = "vrml_account_linked"
	EventVRMLAccountResync      = "vrml_account_resync"
	EventMatchData              = "match_data"
	matchDataDatabaseName       = "nevr"
	matchDataCollectionName     = "match_data"
)

type EventDispatch struct {
	sync.Mutex

	ctx    context.Context
	logger runtime.Logger
	nk     runtime.NakamaModule
	db     *sql.DB
	mongo  *mongo.Client

	queue         chan *api.Event
	matchJournals map[MatchID]*MatchDataJournal
	cache         *sync.Map
	vrmlVerifier  *VRMLVerifier
}

func NewEventDispatch(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer, mongoClient *mongo.Client) (*EventDispatch, error) {

	vrmlVerifier, err := NewVRMLVerifier(ctx, logger, db, nk, initializer, dg)
	if err != nil {
		return nil, fmt.Errorf("failed to create VRML verifier: %w", err)
	}

	dispatch := &EventDispatch{
		ctx:    ctx,
		logger: logger,
		db:     db,
		mongo:  mongoClient,
		nk:     nk,

		queue:         make(chan *api.Event, 100),
		matchJournals: make(map[MatchID]*MatchDataJournal),
		cache:         &sync.Map{},
		vrmlVerifier:  vrmlVerifier,
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-dispatch.queue:

				dispatch.processEvent(ctx, logger, evt)

			case <-time.After(30 * time.Second):

				inserts := make([]any, 0, len(dispatch.matchJournals))

				for k, v := range dispatch.matchJournals {
					if time.Since(v.UpdatedAt) > 1*time.Minute {
						delete(dispatch.matchJournals, k)
						inserts = append(inserts, v)
					}
				}
				if len(inserts) == 0 {
					continue
				}
				// Push to mongo
				collection := dispatch.mongo.Database(matchDataDatabaseName).Collection(matchDataCollectionName)
				if _, err := collection.InsertMany(ctx, inserts); err != nil {
					logger.Error("failed to insert match data: %v", err)
				}
			}
		}
	}()

	return dispatch, nil

}

func (h *EventDispatch) eventFn(ctx context.Context, logger runtime.Logger, evt *api.Event) {
	logger.WithField("event", evt.Name).Debug("received event")
	select {
	case h.queue <- evt:
	case <-ctx.Done():
		logger.Warn("context cancelled")
	case <-time.After(3 * time.Second):
		logger.Warn("event queue full")
	}
}

func (h *EventDispatch) processEvent(ctx context.Context, logger runtime.Logger, evt *api.Event) {

	eventMap := map[string]func(context.Context, runtime.Logger, *api.Event) error{
		EventLobbySessionAuthorized: h.handleLobbyAuthorized,
		EventUserLogin:              h.handleUserLogin,
		EventVRMLAccountLinked:      h.handleVRMLAccountLinked,
		EventVRMLAccountResync:      h.handleVRMLAccountLinked,
		EventAccountUpdated:         h.eventSessionEnd,
		EventSessionStart:           h.eventSessionStart,
		EventMatchData:              h.handleMatchEvent,
	}

	fields := map[string]any{
		"event":   evt.Name,
		"handler": fmt.Sprintf("%T", h),
	}

	for k, v := range evt.Properties {
		if k == "password" || k == "token" {
			continue
		}

		fields[k] = v
	}

	logger = logger.WithFields(fields)

	if fn, ok := eventMap[evt.Name]; ok {
		if err := fn(ctx, logger, evt); err != nil {
			logger.Error("failed to handle event: %v", err)
		}
	} else {
		logger.Warn("unhandled event: %s", evt.Name)
	}
}

func (h *EventDispatch) eventSessionStart(ctx context.Context, logger runtime.Logger, evt *api.Event) error {
	logger.Debug("process event session start: %+v", evt.Properties)
	return nil
}

func (h *EventDispatch) eventSessionEnd(ctx context.Context, logger runtime.Logger, evt *api.Event) error {
	logger.Debug("process event session end: %+v", evt.Properties)
	return nil
}

func (h *EventDispatch) handleLobbyAuthorized(ctx context.Context, logger runtime.Logger, evt *api.Event) error {
	h.Lock()
	defer h.Unlock()
	groupID := evt.Properties["group_id"]
	userID := evt.Properties["user_id"]
	sessionID := evt.Properties["session_id"]

	var err error
	var gg *GuildGroup

	key := groupID + ":*GuildGroup"

	if v, ok := h.cache.Load(key); ok {
		gg = v.(*GuildGroup)
	} else {
		// Notify the group of the login, if it's an alternate
		gg, err = GuildGroupLoad(ctx, h.nk, groupID)
		if err != nil {
			return fmt.Errorf("failed to load guild group: %w", err)
		}
		// Clear it after thirty seconds
		go func() {
			h.cache.Store(key, gg)
			<-time.After(30 * time.Second)
			h.cache.Delete(key)
		}()
	}

	// Get the users session
	_nk := h.nk.(*RuntimeGoNakamaModule)

	s := _nk.sessionRegistry.Get(uuid.FromStringOrNil(sessionID))
	if s == nil {
		return fmt.Errorf("failed to get session")
	}

	params, ok := LoadParams(s.Context())
	if !ok {
		return fmt.Errorf("failed to load params")
	}

	loginHistory, err := LoginHistoryLoad(ctx, h.nk, userID)
	if err != nil {
		logger.Error("error loading login history: %v", err)
		return fmt.Errorf("failed to load login history: %w", err)
	}

	firstIDs := make([]string, 0, len(loginHistory.AlternateMap))

	displayAuditMessage := false
	if updated := loginHistory.NotifyGroup(groupID, gg.AlternateAccountNotificationExpiry); updated {
		displayAuditMessage = true
	}

	for k := range loginHistory.AlternateMap {
		firstIDs = append(firstIDs, k)
	}

	firstDegree := make([]string, 0, len(firstIDs))
	if len(firstIDs) > 0 {
		firstDegreeAccounts, err := h.nk.AccountsGetId(ctx, firstIDs)
		if err != nil {
			return fmt.Errorf("failed to get alternate accounts: %w", err)
		}

		for _, a := range firstDegreeAccounts {
			if a.User.Id == userID {
				continue
			}

			s := "<@" + a.CustomId + ">"
			if gg.IsSuspended(s, &params.xpID) {
				s = s + " (suspended)"
				displayAuditMessage = true
			}

			if a.DisableTime != nil {
				s = s + " (disabled)"
				displayAuditMessage = false
			}
			if ok, expiry := gg.IsTimedOut(s); ok {
				s = fmt.Sprintf("%s (timeout expires <t:%d:R>", s, expiry.UTC().Unix())
				displayAuditMessage = true
			}

			firstDegree = append(firstDegree, s)
		}

		secondDegree := make([]string, 0, len(loginHistory.SecondDegreeAlternates))

		if len(loginHistory.SecondDegreeAlternates) > 0 {

			secondDegreeAccounts, err := h.nk.AccountsGetId(ctx, loginHistory.SecondDegreeAlternates)
			if err != nil {
				return fmt.Errorf("failed to get alternate accounts: %w", err)
			}

			for _, a := range secondDegreeAccounts {
				// Check if any are banned, or currently suspended by the guild
				if a.User.Id == userID || slices.Contains(firstIDs, a.User.Id) {
					continue
				}
				s := "<@" + a.CustomId + ">"
				if gg.IsSuspended(s, nil) {
					s = s + " (suspended)"
				} else if a.DisableTime != nil {
					s = s + " (disabled)"
				}

				secondDegree = append(secondDegree, s)
			}
		}

		if len(firstDegree)+len(secondDegree) > 0 && displayAuditMessage {
			content := fmt.Sprintf("<@%s> detected as a likely alternate of: %s", params.DiscordID(), strings.Join(firstDegree, ", "))

			if len(secondDegree) > 0 {
				content += fmt.Sprintf("\nSecond degree (possible) alternates: %s\n", strings.Join(secondDegree, ", "))
			}
			_, _ = s.(*sessionWS).evrPipeline.appBot.LogAuditMessage(ctx, groupID, content, false)
		}

		if err := loginHistory.Store(ctx, h.nk); err != nil {
			return fmt.Errorf("failed to store login history: %w", err)
		}
	}

	return nil
}

func (h *EventDispatch) handleUserLogin(ctx context.Context, logger runtime.Logger, evt *api.Event) error {
	h.Lock()
	defer h.Unlock()
	userID := evt.Properties["user_id"]

	loginHistory, err := LoginHistoryLoad(ctx, h.nk, userID)
	if err != nil {
		return fmt.Errorf("failed to load login history: %w", err)
	}
	if err := loginHistory.UpdateAlternates(ctx, h.nk); err != nil {
		return fmt.Errorf("failed to update alternates: %w", err)
	}

	if err := loginHistory.Store(ctx, h.nk); err != nil {
		return fmt.Errorf("failed to store login history: %w", err)
	}

	return nil
}

func (h *EventDispatch) handleVRMLAccountLinked(ctx context.Context, logger runtime.Logger, evt *api.Event) error {

	return h.vrmlVerifier.VerifyUser(evt.Properties["user_id"], evt.Properties["token"])
}

func (h *EventDispatch) handleMatchEvent(ctx context.Context, logger runtime.Logger, evt *api.Event) error {

	matchID, err := MatchIDFromString(evt.Properties["match_id"])
	if err != nil {
		return fmt.Errorf("failed to parse match ID: %w", err)
	}

	var data = map[string]any{}

	if err := json.Unmarshal([]byte(evt.Properties["payload"]), &data); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	j, ok := h.matchJournals[matchID]
	if !ok {
		h.matchJournals[matchID] = NewMatchDataJournal(matchID)
		j = h.matchJournals[matchID]
	}

	j.Events = append(j.Events, &MatchDataJournalEntry{
		CreatedAt: time.Now().UTC(),
		Data:      data,
	})
	j.UpdatedAt = time.Now().UTC()

	return nil
}
