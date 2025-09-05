package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/go-redis/redis"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.mongodb.org/mongo-driver/mongo"
)

type EventProcessor interface {
	Process(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error
}

const (
	EventLobbySessionAuthorized = "lobby_session_authorized"
	EventSessionStart           = "session_start"
	EventSessionEnd             = "session_end"
	EventMatchData              = "match_data"
	matchDataDatabaseName       = "nevr"
	matchDataCollectionName     = "match_data"
)

type EventDispatcher struct {
	sync.Mutex

	ctx             context.Context
	logger          runtime.Logger
	nk              runtime.NakamaModule
	db              *sql.DB
	mongo           *mongo.Client
	dg              *discordgo.Session
	sessionRegistry server.SessionRegistry
	matchRegistry   server.MatchRegistry

	statisticsQueue *StatisticsQueue
	vrmlScanQueue   *VRMLScanQueue

	mongoClient          *mongo.Client
	redisClient          *redis.Client
	eventUnmarshaler     func(event *api.Event) (any, error)
	queue                chan *api.Event
	matchJournals        map[MatchID]*MatchDataJournal
	cache                *sync.Map
	playerAuthorizations map[string]map[string]struct{} // map[sessionID]map[groupID]struct{}
}

func NewEventDispatch(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, sessionRegistry server.SessionRegistry, matchRegistry server.MatchRegistry, mongoClient *mongo.Client, redisClient *redis.Client, dg *discordgo.Session, statisticsQueue *StatisticsQueue, vrmlScanQueue *VRMLScanQueue) (*EventDispatcher, error) {

	dispatch := &EventDispatcher{
		ctx:             ctx,
		logger:          logger,
		db:              db,
		mongo:           mongoClient,
		nk:              nk,
		dg:              dg,
		sessionRegistry: sessionRegistry,
		matchRegistry:   matchRegistry,

		vrmlScanQueue:   vrmlScanQueue,
		statisticsQueue: statisticsQueue,

		redisClient: redisClient,
		mongoClient: mongoClient,

		queue:                make(chan *api.Event, 100),
		matchJournals:        make(map[MatchID]*MatchDataJournal),
		cache:                &sync.Map{},
		playerAuthorizations: make(map[string]map[string]struct{}),
	}

	dispatch.eventUnmarshaler = dispatch.unmarshalEventFactory([]any{
		&EventUserAuthenticated{},
		&EventVRMLAccountLink{},
		&EventRemoteLogSet{},
	})

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-dispatch.queue:

				doneCh := make(chan struct{})

				go func() {
					defer close(doneCh)
					dispatch.processEvent(ctx, logger, evt)
					doneCh <- struct{}{}
				}()
				select {
				case <-ctx.Done():
					logger.Warn("context canceled while processing event")
					return
				case <-doneCh:
					logger.WithField("event", evt.Name).Debug("processed event")
				case <-time.After(5 * time.Second):
					logger.WithField("event", evt.Name).Debug("event processing took too long, skipping")
					continue
				}
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
				if dispatch.mongo != nil {
					// Push to mongo
					collection := dispatch.mongo.Database(matchDataDatabaseName).Collection(matchDataCollectionName)
					if _, err := collection.InsertMany(ctx, inserts); err != nil {
						logger.WithField("error", err).Error("failed to insert match data")
					}
				}
			}
		}
	}()

	return dispatch, nil

}

func (h *EventDispatcher) EventFn(ctx context.Context, logger runtime.Logger, evt *api.Event) {
	select {
	case h.queue <- evt:
		logger.WithField("event", evt.Name).Debug("event queued for processing")
	default:
		logger.Warn("event queue full")
	}
}

func (h *EventDispatcher) unmarshalEventFactory(events []any) func(event *api.Event) (any, error) {
	eventMap := make(map[string]any)
	for _, e := range events {
		eventMap[fmt.Sprintf("%T", e)] = e
	}
	return func(event *api.Event) (any, error) {
		if e, ok := eventMap[event.Name]; ok {
			if err := json.Unmarshal([]byte(event.Properties["payload"]), e); err != nil {
				return nil, fmt.Errorf("failed to unmarshal event payload: %v", err)
			}
			return e, nil
		}
		return nil, fmt.Errorf("unknown event type: %s", event.Name)
	}
}

func (h *EventDispatcher) processEvent(ctx context.Context, logger runtime.Logger, evt *api.Event) {

	eventMap := map[string]func(context.Context, runtime.Logger, *api.Event) error{
		EventSessionStart:           h.eventSessionStart,
		EventSessionEnd:             h.eventSessionEnd,
		EventLobbySessionAuthorized: h.handleLobbyAuthorized,
		EventMatchData:              h.handleMatchEvent,
	}

	if _, ok := eventMap[evt.Name]; !ok {
		if e, err := h.eventUnmarshaler(evt); err != nil {
			logger.WithField("error", err).Warn("failed to unmarshal event")
		} else if e != nil {
			return
		}
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
			logger.WithField("error", err).Error("failed to handle event")
		}
	} else {
		logger.Warn("unhandled event: %s", evt.Name)
	}
}

func (h *EventDispatcher) eventSessionStart(ctx context.Context, logger runtime.Logger, evt *api.Event) error {
	logger.Debug("process event session start: %+v", evt.Properties)
	return nil
}

func (h *EventDispatcher) eventSessionEnd(ctx context.Context, logger runtime.Logger, evt *api.Event) error {
	h.Lock()
	defer h.Unlock()

	delete(h.playerAuthorizations, evt.Properties["session_id"])

	logger.Debug("process event session end: %+v", evt.Properties)
	return nil
}

func (h *EventDispatcher) guildGroup(ctx context.Context, groupID string) (*GuildGroup, error) {
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
func (h *EventDispatcher) auditedSession(groupID, sessionID string) bool {
	h.Lock()
	defer h.Unlock()
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
	userID        string
	username      string
	discordID     string
	isFirstDegree bool
}

func (h *EventDispatcher) handleLobbyAuthorized(ctx context.Context, logger runtime.Logger, evt *api.Event) error {

	if isNew := h.auditedSession(evt.Properties["group_id"], evt.Properties["session_id"]); !isNew {
		return nil
	}

	var (
		err          error
		groupID      = evt.Properties["group_id"]
		userID       = evt.Properties["user_id"]
		loginHistory = NewLoginHistory(userID)
		gg           *GuildGroup
	)

	if gg, err = h.guildGroup(ctx, groupID); err != nil || gg == nil {
		return fmt.Errorf("failed to load guild group: %w", err)
	}

	if err = StorableReadNk(ctx, h.nk, userID, loginHistory, true); err != nil {
		return fmt.Errorf("failed to load login history: %w", err)
	}

	if updated := loginHistory.NotifyGroup(groupID, gg.AlternateAccountNotificationExpiry); updated {

		if err := StorableWriteNk(ctx, h.nk, userID, loginHistory); err != nil {
			logger.WithField("error", err).Warn("failed to store login history after notify group update")
		}

		// Check for guild suspensions on alts
		var (
			firstIDs, secondIDs = loginHistory.AlternateIDs()
		)

		accounts, err := h.nk.AccountsGetId(ctx, append(firstIDs, secondIDs...))
		if err != nil {
			return fmt.Errorf("failed to get alternate accounts: %w", err)
		}

		alternates := make(map[string]*compactAlternate, len(accounts))
		for _, a := range accounts {
			alternates[a.User.Id] = &compactAlternate{
				userID:        a.User.Id,
				username:      a.User.Username,
				discordID:     a.CustomId,
				isFirstDegree: slices.Contains(firstIDs, a.User.Id),
			}
		}

		content := h.alternateLogLineFormatter(userID, alternates)

		if _, err := AuditLogSendGuild(h.dg, gg, content); err != nil {
			return fmt.Errorf("failed to send audit message: %w", err)
		}
	}

	return nil
}

func (h *EventDispatcher) alternateLogLineFormatter(userID string, alternates map[string]*compactAlternate) string {
	var (
		firstDegreeStrs  = make([]string, 0, len(alternates))
		secondDegreeStrs = make([]string, 0, len(alternates))
	)

	for _, a := range alternates {
		if a.userID == userID {
			continue
		}
		// Check if any are banned, or currently suspended by the guild
		s := fmt.Sprintf("<@%s> (%s)", a.discordID, a.username)
		addons := make([]string, 0, 2)

		if len(addons) > 0 {
			s += " *[" + strings.Join(addons, ", ") + "]*"
		}

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

func (h *EventDispatcher) handleMatchEvent(ctx context.Context, logger runtime.Logger, evt *api.Event) error {
	h.Lock()
	defer h.Unlock()
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
	if message == "" {
		return nil, nil
	}
	content := fmt.Sprintf("[`%s/%s`] %s", gg.Name(), gg.GuildID, message)
	if err := AuditLogSend(dg, ServiceSettings().ServiceAuditChannelID, content); err != nil {
		return nil, fmt.Errorf("failed to send service audit message: %w", err)
	}

	if err := AuditLogSend(dg, gg.AuditChannelID, message); err != nil {
		return nil, fmt.Errorf("failed to send service audit message: %w", err)
	}

	return nil, nil
}

func AuditLogSend(dg *discordgo.Session, channelID, content string) error {
	if content == "" {
		return nil
	}
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

func ScheduleKick(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, dg *discordgo.Session, loginHistory *LoginHistory, userID string, guildGroup *GuildGroup, delay time.Duration) {
	go func() {
		// Set random time to disable and kick player
		var (
			firstIDs, _ = loginHistory.AlternateIDs()
			altNames    = make([]string, 0, len(loginHistory.AlternateMatches))
			accountMap  = make(map[string]*api.Account, len(loginHistory.AlternateMatches))
			err         error
		)

		if accounts, err := nk.AccountsGetId(ctx, append(firstIDs, userID)); err != nil {
			logger.WithField("error", err).Error("failed to get alternate accounts")
			return
		} else {
			for _, a := range accounts {
				accountMap[a.User.Id] = a
				altNames = append(altNames, fmt.Sprintf("<@%s> (%s)", a.CustomId, a.User.Username))
			}
		}

		if len(altNames) == 0 || accountMap[userID] == nil {
			logger.WithField("error", err).Error("failed to get alternate accounts")
			return
		}

		slices.Sort(altNames)
		altNames = slices.Compact(altNames)

		// Send audit log message
		content := fmt.Sprintf("<@%s> (%s) has disabled alternates, disconnecting session(s) in %d seconds.\n%s", accountMap[userID].CustomId, accountMap[userID].User.Username, int(delay.Seconds()), strings.Join(altNames, ", "))
		AuditLogSendGuild(dg, guildGroup, content)

		logger.WithField("delay", delay).Info("kicking (with delay) user %s has disabled alternates", userID)

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		presences, err := nk.StreamUserList(StreamModeService, userID, "", "", false, true)
		if err != nil {
			logger.WithField("error", err).Warn("failed to get user presences")
			return
		}

		matchLabels := make([]*MatchLabel, 0, len(presences))
		for _, p := range presences {
			if p.GetStatus() != "" {
				mid := MatchIDFromStringOrNil(p.GetStatus())
				if mid.IsNil() {
					continue
				}
				label, err := MatchLabelByID(ctx, nk, mid)
				if err != nil {
					logger.Warn("failed to get match label", "error", err)
					continue
				}

				matchLabels = append(matchLabels, label)
			}
		}

		suspendedGroupIDs := []string{
			guildGroup.Group.Id,
		}
		if guildGroup.SuspensionInheritanceGroupIDs != nil {
			suspendedGroupIDs = append(suspendedGroupIDs, guildGroup.SuspensionInheritanceGroupIDs...)
		}

		actions := make([]string, 0, len(matchLabels))
		doDisconnect := false
		for _, label := range matchLabels {
			// Kick the player from the match
			if slices.Contains(suspendedGroupIDs, label.GetGroupID().String()) {
				doDisconnect = true
				actions = append(actions, fmt.Sprintf("kicked from [%s](https://echo.taxi/spark://c/%s) session.", label.Mode.String(), strings.ToUpper(label.ID.UUID.String())))
				if err := KickPlayerFromMatch(ctx, nk, label.ID, userID); err != nil {
					actions = append(actions, fmt.Sprintf("failed to kick player from [%s](https://echo.taxi/spark://c/%s) (error: %s)", label.Mode.String(), strings.ToUpper(label.ID.UUID.String()), err.Error()))
					continue
				}
			}
		}

		if doDisconnect {
			// Disconnect the user from the session
			if c, err := DisconnectUserID(ctx, nk, userID, true, true, false); err != nil {
				logger.WithField("error", err).Error("failed to disconnect user")
			} else {
				logger.Info("user %s disconnected: %v sessions", userID, c)
			}
		}

		// Send audit log message
		AuditLogSend(dg, ServiceSettings().ServiceAuditChannelID, fmt.Sprintf("Disconnected user %s (%s) from %d sessions:\n%s", userID, accountMap[userID].User.Username, len(matchLabels), strings.Join(actions, "\n")))

	}()
}
