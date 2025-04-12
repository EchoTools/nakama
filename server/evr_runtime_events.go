package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.mongodb.org/mongo-driver/mongo"
)

const (
	EventLobbySessionAuthorized = "lobby_session_authorized"
	EventUserLogin              = "user_login"
	EventSessionStart           = "session_start"
	EventSessionEnd             = "session_end"
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
	dg     *discordgo.Session

	queue                chan *api.Event
	matchJournals        map[MatchID]*MatchDataJournal
	cache                *sync.Map
	playerAuthorizations map[string]map[string]struct{} // map[sessionID]map[groupID]struct{}
	vrmlVerifier         *VRMLVerifier
}

func NewEventDispatch(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer, mongoClient *mongo.Client, dg *discordgo.Session) (*EventDispatch, error) {

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
		dg:     dg,

		queue:                make(chan *api.Event, 100),
		matchJournals:        make(map[MatchID]*MatchDataJournal),
		cache:                &sync.Map{},
		vrmlVerifier:         vrmlVerifier,
		playerAuthorizations: make(map[string]map[string]struct{}),
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
		logger.Warn("context canceled")
	case <-time.After(3 * time.Second):
		logger.Warn("event queue full")
	}
}

func (h *EventDispatch) processEvent(ctx context.Context, logger runtime.Logger, evt *api.Event) {

	eventMap := map[string]func(context.Context, runtime.Logger, *api.Event) error{
		EventSessionStart:           h.eventSessionStart,
		EventSessionEnd:             h.eventSessionEnd,
		EventUserLogin:              h.handleUserLogin,
		EventLobbySessionAuthorized: h.handleLobbyAuthorized,
		EventVRMLAccountLinked:      h.handleVRMLAccountLinked,
		EventVRMLAccountResync:      h.handleVRMLAccountLinked,
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
	h.Lock()
	defer h.Unlock()

	delete(h.playerAuthorizations, evt.Properties["session_id"])

	logger.Debug("process event session end: %+v", evt.Properties)
	return nil
}

func (h *EventDispatch) guildGroup(ctx context.Context, groupID string) (*GuildGroup, error) {
	var (
		err error
		key = groupID + ":*GuildGroup"
		gg  *GuildGroup
	)
	if v, ok := h.cache.Load(key); ok && v != nil {
		gg = v.(*GuildGroup)
	} else {
		// Notify the group of the login, if it's an alternate
		gg, err = GuildGroupLoad(ctx, h.nk, groupID)
		if err != nil || gg == nil {
			return nil, fmt.Errorf("failed to load guild group: %w", err)
		}
		h.cache.Store(key, gg)
		go time.AfterFunc(30*time.Second, func() { h.cache.Delete(key) }) // Cleanup cache after 30 seconds
	}
	return gg, nil
}

// auditedSession marks the session as audited for the group
// and returns true if it was a new session.
func (h *EventDispatch) auditedSession(groupID, sessionID string) bool {
	if h.playerAuthorizations[sessionID] == nil {
		h.playerAuthorizations[sessionID] = make(map[string]struct{})
	}
	_, found := h.playerAuthorizations[sessionID][groupID]
	if !found {
		h.playerAuthorizations[sessionID][groupID] = struct{}{}
	}
	return !found
}

type compactAlternate struct {
	userID           string
	username         string
	discordID        string
	isDisabled       bool
	suspensionExpiry time.Time
	isFirstDegree    bool
}

func (c compactAlternate) String() string {
	return fmt.Sprintf("suspended until <t:%d:R>", c.suspensionExpiry.Unix())
}

func (h *EventDispatch) handleLobbyAuthorized(ctx context.Context, logger runtime.Logger, evt *api.Event) error {
	h.Lock()
	defer h.Unlock()

	if isNew := h.auditedSession(evt.Properties["group_id"], evt.Properties["session_id"]); !isNew {
		return nil
	}

	var (
		err          error
		groupID      = evt.Properties["group_id"]
		userID       = evt.Properties["user_id"]
		loginHistory = &LoginHistory{}
		gg           *GuildGroup

		displayAuditMessage = false
	)

	if gg, err = h.guildGroup(ctx, groupID); err != nil || gg == nil {
		return fmt.Errorf("failed to load guild group: %w", err)
	}

	if err = StorageRead(ctx, h.nk, userID, loginHistory, true); err != nil {
		return fmt.Errorf("failed to load login history: %w", err)
	}

	if updated := loginHistory.NotifyGroup(groupID, gg.AlternateAccountNotificationExpiry); updated {
		displayAuditMessage = true
		if _, err := StorageWrite(ctx, h.nk, userID, loginHistory); err != nil {
			return fmt.Errorf("failed to store login history: %w", err)
		}
	}

	var (
		firstIDs, secondaryIDs = loginHistory.AlternateIDs()
		alternateIDs           = append(append(firstIDs, secondaryIDs...), userID)
		suspendedUserIDs       = make(map[string]time.Time, 0)
		alternates             = make(map[string]*compactAlternate, len(alternateIDs))
	)

	if len(alternateIDs) == 0 {
		return nil
	}

	// Check for guild suspensions
	if guildRecords, err := EnforcementSearch(ctx, h.nk, groupID, alternateIDs); err != nil && len(guildRecords) > 0 {
		for _, records := range guildRecords {
			if suspensions := records.ActiveSuspensions(); len(suspensions) > 0 {
				suspendedUserIDs[records.UserID] = suspensions[0].SuspensionExpiry
				displayAuditMessage = true
			}
		}
	}

	// Get the accounts for the alternates, and check if they are disabled
	if accounts, err := h.nk.AccountsGetId(ctx, alternateIDs); err != nil {
		return fmt.Errorf("failed to get alternate accounts: %w", err)
	} else {
		for _, a := range accounts {
			_, isFirstDegree := loginHistory.AlternateMap[a.User.Id]
			alternates[a.User.Id] = &compactAlternate{
				userID:           a.User.Id,
				username:         a.User.Username,
				discordID:        a.CustomId,
				suspensionExpiry: suspendedUserIDs[a.User.Id],
				isFirstDegree:    isFirstDegree,
				isDisabled:       a.GetDisableTime() != nil && !a.GetDisableTime().AsTime().IsZero(),
			}
		}
	}

	content := h.alternateLogLineFormatter(userID, alternates)

	if displayAuditMessage {
		if _, err := AuditLogSendGuild(h.dg, gg, content); err != nil {
			return fmt.Errorf("failed to send audit message: %w", err)
		}
	}

	return nil
}

func (h *EventDispatch) alternateLogLineFormatter(userID string, alternates map[string]*compactAlternate) string {
	var (
		firstDegreeStrs  = make([]string, 0, len(alternates))
		secondDegreeStrs = make([]string, 0, len(alternates))
	)

	for _, a := range alternates {
		// Check if any are banned, or currently suspended by the guild
		s := fmt.Sprintf("<@%s> (%s)", a.discordID, a.username)
		addons := make([]string, 0, 2)
		if a.isDisabled {
			addons = append(addons, "disabled")
		}
		if !a.suspensionExpiry.IsZero() {
			addons = append(addons, a.String())
		}

		s += " *[" + strings.Join(addons, ", ") + "]*"

		if a.isFirstDegree {
			firstDegreeStrs = append(firstDegreeStrs, s)
		} else {
			secondDegreeStrs = append(secondDegreeStrs, s)
		}
	}

	main := alternates[userID]
	if main == nil {
		return ""
	}
	content := fmt.Sprintf("<@%s> (%s) detected as a likely alternate of: %s", main.discordID, main.username, strings.Join(firstDegreeStrs, ", "))

	if len(secondDegreeStrs) > 0 {
		content += fmt.Sprintf("\nSecond degree (possible) alternates: %s\n", strings.Join(secondDegreeStrs, ", "))
	}

	return content
}

func (h *EventDispatch) handleUserLogin(ctx context.Context, logger runtime.Logger, evt *api.Event) error {
	h.Lock()
	defer h.Unlock()
	userID := evt.Properties["user_id"]

	loginHistory := NewLoginHistory(userID)
	if err := StorageRead(ctx, h.nk, userID, loginHistory, true); err != nil {
		return fmt.Errorf("failed to load login history: %w", err)
	}

	if serviceSettings := ServiceSettings(); serviceSettings != nil && serviceSettings.KickPlayersWithDisabledAlternates {
		if hasDiabledAlts, err := loginHistory.UpdateAlternates(ctx, h.nk); err != nil {
			return fmt.Errorf("failed to update alternates: %w", err)
		} else if hasDiabledAlts {

			go func() {

				// Set random time to disable and kick player
				var (
					firstIDs, _        = loginHistory.AlternateIDs()
					altNames           = make([]string, 0, len(loginHistory.AlternateMap))
					accountMap         = make(map[string]*api.Account, len(loginHistory.AlternateMap))
					delayMin, delayMax = 1, 4
					kickDelay          = time.Duration(delayMin+rand.Intn(delayMax)) * time.Minute
				)

				if accounts, err := h.nk.AccountsGetId(ctx, append(firstIDs, userID)); err != nil {
					logger.Error("failed to get alternate accounts: %v", err)
					return
				} else {
					for _, a := range accounts {
						accountMap[a.User.Id] = a
						altNames = append(altNames, fmt.Sprintf("<@%s> (%s)", a.CustomId, a.User.Username))
					}
				}

				if len(altNames) == 0 || accountMap[userID] == nil {
					logger.Error("failed to get alternate accounts: %v", err)
					return
				}

				slices.Sort(altNames)
				altNames = slices.Compact(altNames)

				// Send audit log message
				content := fmt.Sprintf("<@%s> (%s) has disabled alternates, disconnecting session(s) in %d seconds.\n%s", accountMap[userID].CustomId, accountMap[userID].User.Username, int(kickDelay.Seconds()), strings.Join(altNames, ", "))
				AuditLogSend(dg, serviceSettings.ServiceAuditChannelID, content)

				logger.WithField("delay", kickDelay).Info("kicking (with delay) user %s has disabled alternates", userID)
				<-time.After(kickDelay)
				if c, err := DisconnectUserID(ctx, h.nk, userID, true, true, false); err != nil {
					logger.Error("failed to disconnect user: %v", err)
				} else {
					logger.Info("user %s disconnected: %v sessions", userID, c)
				}
			}()
		}
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

func AuditLogSendGuild(dg *discordgo.Session, gg *GuildGroup, message string) (*discordgo.Message, error) {

	content := fmt.Sprintf("[`%s/%s`] %s", gg.Name(), gg.ID(), message)
	if err := AuditLogSend(dg, ServiceSettings().ServiceAuditChannelID, content); err != nil {
		return nil, fmt.Errorf("failed to send service audit message: %w", err)
	}

	if err := AuditLogSend(dg, gg.AuditChannelID, message); err != nil {
		return nil, fmt.Errorf("failed to send service audit message: %w", err)
	}

	return nil, nil
}

func AuditLogSend(dg *discordgo.Session, channelID, content string) error {
	// replace all <@uuid> mentions with <@discordID>
	if channelID != "" {
		if _, err := dg.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
			Content:         content,
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		}); err != nil {
			return fmt.Errorf("failed to send service audit message: %w", err)
		}
	}
	return nil
}
