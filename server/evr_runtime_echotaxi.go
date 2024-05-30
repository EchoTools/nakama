package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	EchoTaxiPrefix      = "https://echo.taxi/"
	sprockLinkDiscordID = "1102051447597707294"

	EchoTaxiStorageCollection = "EchoTaxi"
	EchoTaxiStorageKey        = "Hail"
	TaxiEmoji                 = "🚕"

	LinkPattern = `(?i)(?P<httpPrefix>https://echo.taxi/|https://sprock.io/)?(?P<appLinkPrefix>(?:spark://[scj]/|Aether://)?)(?P<matchID>[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`
)

var (
	LinkRegex = regexp.MustCompile(LinkPattern)
)

type TrackID struct {
	ChannelID string
	MessageID string
}

type TaxiLinkRegistry struct {
	sync.Mutex
	ctx         context.Context
	ctxCancelFn context.CancelFunc
	nk          runtime.NakamaModule
	logger      runtime.Logger
	dg          *discordgo.Session

	node    string
	tracked map[TrackID]MatchID
}

func NewTaxiLinkRegistry(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, config Config, dg *discordgo.Session) *TaxiLinkRegistry {
	ctx, cancel := context.WithCancel(ctx)

	linkRegistry := &TaxiLinkRegistry{
		ctx:         ctx,
		node:        config.GetName(),
		ctxCancelFn: cancel,
		nk:          nk,
		logger:      logger,
		dg:          dg,
		tracked:     make(map[TrackID]MatchID),
	}

	return linkRegistry
}

func (e *TaxiLinkRegistry) Stop() {
	e.ctxCancelFn()
}

func (r *TaxiLinkRegistry) load(t TrackID) (MatchID, bool) {
	r.Lock()
	defer r.Unlock()
	m, found := r.tracked[t]
	return m, found
}

func (r *TaxiLinkRegistry) store(t TrackID, m MatchID) {
	r.Lock()
	defer r.Unlock()
	r.tracked[t] = m
}

func (r *TaxiLinkRegistry) delete(t TrackID) {
	r.Lock()
	defer r.Unlock()
	delete(r.tracked, t)
}

// React adds a taxi reaction to a message
func (e *TaxiLinkRegistry) React(channelID, messageID string) error {
	err := e.dg.MessageReactionAdd(channelID, messageID, TaxiEmoji)
	if err != nil {
		return err
	}

	// Remove any other taxi reactions (ignoring errors)
	_ = e.Clear(channelID, messageID, false)

	return nil
}

// Track adds a message to the tracker
func (e *TaxiLinkRegistry) Track(matchID MatchID, channelID, messageID string) {

	t := TrackID{
		ChannelID: channelID,
		MessageID: messageID,
	}

	e.store(t, matchID)
}

func (e *TaxiLinkRegistry) Remove(t TrackID) error {

	_, found := e.load(t)
	if !found {
		return nil
	}

	if err := e.Clear(t.ChannelID, t.MessageID, true); err != nil {
		return err
	}

	e.delete(t)
	return nil
}

// Clear removes all taxi reactions
func (e *TaxiLinkRegistry) Clear(channelID, messageID string, all bool) error {

	// List the users that reacted with the taxi emoji
	users, err := e.dg.MessageReactions(channelID, messageID, TaxiEmoji, 100, "", "")
	if err != nil || len(users) == 0 {
		return err
	}

	// Remove all reactions, except the bot's (if all is false)
	for _, user := range users {
		if !all && user.ID == e.dg.State.User.ID {
			continue
		}
		e.logger.Debug("Removing reaction from %s", user.ID)
		// Remove the reaction
		if err = e.dg.MessageReactionRemove(channelID, messageID, TaxiEmoji, user.ID); err != nil {
			return err
		}
	}

	return nil
}

func extractMatchComponents(content string) (httpPrefix, applinkPrefix, matchIDStr string) {
	match := LinkRegex.FindStringSubmatch(content)
	result := make(map[string]string, 2)
	if len(match) == 0 {
		return "", "", ""
	}
	for i, name := range LinkRegex.SubexpNames() {
		result[name] = match[i]
	}
	return result["httpPrefix"], result["appLinkPrefix"], strings.ToLower(result["matchID"])
}

// Process processes a message for taxi reactions and link responses
func (e *TaxiLinkRegistry) Process(channelID, messageID, content string, reactOnly bool) (err error) {

	// Detect a MatchID in the message
	_, _, matchIDStr := extractMatchComponents(content)

	if matchIDStr == "" {
		return nil
	}
	if !strings.Contains(matchIDStr, ".") {
		matchIDStr = matchIDStr + "." + e.node
	}

	matchID := MatchIDFromStringOrNil(matchIDStr)

	if matchID.IsNil() {
		return nil
	}

	// React, and track the message
	if err = e.React(channelID, messageID); err != nil {
		return err
	}

	// Track the message
	e.Track(matchID, channelID, messageID)

	return nil
}

// Prune removes all inactive matches, and their reactions
func (e *TaxiLinkRegistry) Prune() (active, pruned int, err error) {
	e.Lock()
	defer e.Unlock()
	// Check all the tracked matches
	for t, m := range e.tracked {

		// Check if the match is still active
		match, err := e.nk.MatchGet(e.ctx, m.String())
		if err == nil && match != nil {
			active++
			continue
		}

		// If the match is not active, then remove the match from the tracker
		delete(e.tracked, t)

		// Remove all the taxi reactions from the channel messages
		if err := e.Clear(t.ChannelID, t.MessageID, true); err != nil {
			e.logger.Warn("Error clearing taxi reactions: %v", err)
		}
		pruned++
	}
	return active, pruned, nil
}

// Count returns the number of actively tracked URLs
func (e *TaxiLinkRegistry) Count() (cnt int) {
	e.Lock()
	defer e.Unlock()
	return len(e.tracked)
}

func EchoTaxiRuntimeModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) (err error) {
	env := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

	if s, ok := env["DISABLE_DISCORD_RESPONSE_HANDLERS"]; ok && s == "true" {
		return nil
	}

	node := ctx.Value(runtime.RUNTIME_CTX_NODE).(string)

	botToken, ok := env["ECHOTAXI_DISCORD_BOT_TOKEN"]
	if !ok {
		logger.Error("No Discord bot token found in environment variables. Please set ECHOTAXI_DISCORD_BOT_TOKEN.")
		return nil
	}

	dg, err := discordgo.New("Bot " + botToken)

	if err != nil {
		return err
	}
	nkgo := nk.(*RuntimeGoNakamaModule)
	linkRegistry := NewTaxiLinkRegistry(ctx, logger, nk, nkgo.config, dg)

	// Initialize the taxi bot
	taxi := NewTaxiBot(ctx, logger, nk, node, linkRegistry, dg)

	err = taxi.Initialize(dg)
	if err != nil {
		return err
	}

	return nil
}

type TaxiBot struct {
	sync.Mutex
	node   string
	ctx    context.Context
	logger runtime.Logger
	nk     runtime.NakamaModule
	dg     *discordgo.Session

	HailCount    int
	linkRegistry *TaxiLinkRegistry

	queueCh      chan TrackID
	deconflict   *MapOf[string, bool]
	userChannels *MapOf[string, string]

	rateLimiters         *MapOf[string, *rate.Limiter]
	messageRatePerSecond rate.Limit
	messageBurst         int
}

func NewTaxiBot(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, node string, linkRegistry *TaxiLinkRegistry, dg *discordgo.Session) *TaxiBot {
	taxi := &TaxiBot{
		node:   node,
		ctx:    ctx,
		logger: logger,
		nk:     nk,
		dg:     dg,

		linkRegistry:         linkRegistry,
		HailCount:            0,
		deconflict:           &MapOf[string, bool]{},   // sprockLinkChannels maps discord channel ids to boolean
		userChannels:         &MapOf[string, string]{}, // userChannels maps discord user ids to channel ids
		queueCh:              make(chan TrackID, 5),
		rateLimiters:         &MapOf[string, *rate.Limiter]{},
		messageRatePerSecond: 0.1,
		messageBurst:         3,
	}

	// Start the processing/deconflict queue
	err := taxi.loadCount()
	if err != nil {
		logger.Warn("Error loading count: %s", err.Error())
	}

	go func() {
		statusTicker := time.NewTicker(10 * time.Second)
		storeHailCountTicker := time.NewTicker(5 * time.Minute)
		messagePruneTicker := time.NewTicker(30 * time.Second)
		limiterTicker := time.NewTicker(60 * time.Minute)
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-taxi.queueCh:
				// Load the message
				m, err := dg.ChannelMessage(t.ChannelID, t.MessageID)
				if m == nil || err != nil {
					continue
				}
				// Process the message
				linkRegistry.Process(t.ChannelID, t.MessageID, m.Content, false)
			case <-statusTicker.C:
				// Update the status
				if err := linkRegistry.setStatus(taxi.HailCount, taxi.linkRegistry.Count()); err != nil {
					logger.Warn("Error setting status: %s", err.Error())
				}
			case <-storeHailCountTicker.C:
				// Save the hail count
				if err := taxi.saveCount(); err != nil {
					logger.Warn("Error saving count: %s", err.Error())
				}

			case <-messagePruneTicker.C:
				// do housekeeping on a tick to remove inactive matches
				_, _, _ = linkRegistry.Prune()

			case <-limiterTicker.C:
				// Clear the rate limiters for the channels
				taxi.pruneRateLimiters()
			}
		}
	}()

	return taxi
}

func (e *TaxiBot) Initialize(dg *discordgo.Session) error {

	dg.Identify.Intents |= discordgo.IntentGuildMessages
	dg.Identify.Intents |= discordgo.IntentGuildMessageReactions
	dg.Identify.Intents |= discordgo.IntentDirectMessages
	dg.Identify.Intents |= discordgo.IntentDirectMessageReactions
	dg.Identify.Intents |= discordgo.IntentMessageContent

	dg.StateEnabled = true
	dg.AddHandler(e.handleMessageCreate)
	dg.AddHandler(e.handleMessageReactionAdd)

	err := dg.Open()
	if err != nil {
		return fmt.Errorf("Error opening EchoTaxi connection to Discord: %v", err)
	}

	e.logger.Info("Initialized EchoTaxi runtime module.")
	return nil
}

func (e *TaxiBot) loadLimiter(channelID string) *rate.Limiter {
	limiter, found := e.rateLimiters.Load(channelID)
	if !found {
		limiter = rate.NewLimiter(e.messageRatePerSecond, e.messageBurst)
		e.rateLimiters.Store(channelID, limiter)
	}
	return limiter
}

func (e *TaxiBot) pruneRateLimiters() {
	e.rateLimiters.Range(func(key string, value *rate.Limiter) bool {
		if value.Tokens() >= float64(e.messageBurst) {
			e.rateLimiters.Delete(key)
		}
		return true
	})
}

func (e *TaxiBot) handleMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}
	logger := e.logger.WithFields(map[string]interface{}{
		"func": "handleMessageCreate",
		"msg":  m,
	})

	httpPrefix, applinkPrefix, matchID, label, err := e.getMatchFromLink(m.Content)
	if err != nil {
		logger.Warn("Error checking content: %s", err.Error())
		return
	}

	if matchID.IsNil() || label == nil {
		return
	}

	// Get the channel rate limiter
	limiter := e.loadLimiter(m.ChannelID)
	if !limiter.Allow() {
		logger.Warn("Rate limited message")
		return
	}

	trackID := TrackID{
		ChannelID: m.ChannelID,
		MessageID: m.ID,
	}

	if m.Author.ID == sprockLinkDiscordID {
		// If the message is from SprockLink, then deconflict
		e.deconflict.Store(m.ChannelID, true)
		// Send this trackID to the queue
		e.queueCh <- trackID
	} else {
		reactOnly, isDeconflicted := e.deconflict.LoadOrStore(m.ChannelID, false)
		if isDeconflicted && reactOnly {
			// If the channel is deconflicted, then ignore the message
			return
		}
		if !isDeconflicted {
			// If the channel is not deconflicted, then wait for the deconflict
			<-time.After(2 * time.Second)
			reactOnly, isDeconflicted := e.deconflict.Load(m.ChannelID)
			if isDeconflicted && reactOnly {
				// Delete the deconfliction
				e.deconflict.Delete(m.ChannelID)
				return
			}
		}
	}

	// Find the app link
	// If the link is not clickable, extract the app link
	if httpPrefix != "" {
		// Just react and track this message
		e.queueCh <- trackID
		return
	}

	if applinkPrefix == "" {
		// If the app link is not present, then add spark
		applinkPrefix = "spark://c/"
	}

	// Replace the MatchID with the uppercased one
	matchIDStr := strings.ToUpper(matchID.UUID().String())

	appURL := fmt.Sprintf("%s%s", applinkPrefix, matchIDStr)
	// Try to respond in the channel with a "clickable" link

	r, err := e.dg.ChannelMessageSend(m.ChannelID, EchoTaxiPrefix+appURL)
	if err == nil {
		// Track the response message
		trackID.MessageID = r.ID
	} else {
		logger.Warn("Error sending message: %v", err)
	}
	// Send it for reaction
	e.queueCh <- trackID

}

// setStatus updates the status of the bot
func (e *TaxiLinkRegistry) setStatus(total, active int) error {

	// Use the taxi emoji to indicate the number of active taxis
	status := fmt.Sprintf("%s x %d (%d)", TaxiEmoji, total, active)

	// Update the status
	if err := e.dg.UpdateGameStatus(0, status); err != nil {
		return fmt.Errorf("Error setting status: %v", err)
	}

	return nil
}

type EchoTaxiStorageData struct {
	HailCount int `json:"hail_count"`
}

func (e *TaxiBot) saveCount() error {

	data := EchoTaxiStorageData{
		HailCount: e.HailCount,
	}
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("Error marshalling data: %s", err.Error())
	}

	// Save the count
	_, err = e.nk.StorageWrite(e.ctx, []*runtime.StorageWrite{
		{
			Collection:      EchoTaxiStorageCollection,
			Key:             EchoTaxiStorageKey,
			UserID:          uuid.Nil.String(),
			Value:           string(jsonData),
			PermissionRead:  1,
			PermissionWrite: 1,
		},
	})
	if err != nil {
		return fmt.Errorf("Error saving count: %s", err.Error())
	}
	return nil
}

func (e *TaxiBot) loadCount() error {
	// Load the count
	objs, err := e.nk.StorageRead(e.ctx, []*runtime.StorageRead{
		{
			Collection: EchoTaxiStorageCollection,
			Key:        EchoTaxiStorageKey,
			UserID:     uuid.Nil.String(),
		},
	})
	if err != nil {
		return fmt.Errorf("Error loading count: %s", err.Error())
	}
	if len(objs) == 0 {
		e.HailCount = 0
	}
	data := EchoTaxiStorageData{}
	err = json.Unmarshal([]byte(objs[0].Value), &data)
	if err != nil {
		return fmt.Errorf("Error unmarshalling data: %s", err.Error())
	}
	e.HailCount = data.HailCount
	return nil
}

// Check a message for a MatchID
func (e *TaxiBot) getMatchFromLink(content string) (httpPrefix, appLinkPrefix string, matchID MatchID, label *EvrMatchState, err error) {

	httpPrefix, appLinkPrefix, matchIDStr := extractMatchComponents(content)

	if !strings.Contains(matchIDStr, ".") {
		matchIDStr = matchIDStr + "." + e.node
	}

	matchID = MatchIDFromStringOrNil(matchIDStr)

	if matchID.IsNil() {
		return "", "", NilMatchID, nil, nil
	}

	// Check if the match is in progress
	match, err := e.nk.MatchGet(e.ctx, matchID.String())
	if err != nil {
		return "", "", NilMatchID, nil, err
	}
	if match == nil {
		return "", "", NilMatchID, nil, nil
	}

	// Unmarshal the label
	label = &EvrMatchState{}
	err = json.Unmarshal([]byte(match.GetLabel().Value), label)
	if err != nil {
		return "", "", NilMatchID, nil, err
	}

	return httpPrefix, appLinkPrefix, matchID, label, nil
}

// Hail sets the next match for a user
func (e *TaxiBot) Hail(logger runtime.Logger, discordID string, matchID MatchID) error {

	// Get the nakama user id from the discord user id
	userID, _, _, err := e.nk.AuthenticateCustom(e.ctx, discordID, "", true)
	if err != nil {
		return fmt.Errorf("Error getting user id from discord id: %s", err.Error())
	}

	// Increment the hail count
	if !matchID.IsNil() {
		e.HailCount++
	}
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Second)
	defer cancel()
	settings, err := LoadMatchmakingSettings(ctx, logger, e.nk, userID)
	if err != nil {
		return fmt.Errorf("Error loading matchmaking config: %s", err.Error())
	}

	// Update the NextMatchID
	settings.NextMatchToken = MatchToken(matchID.String())

	if err := StoreMatchmakingSettings(ctx, logger, e.nk, settings, userID); err != nil {
		return fmt.Errorf("Error storing matchmaking config: %s", err.Error())
	}

	return nil
}

// handleMessageReactionAdd handles the reaction add event
// It checks if the reaction is a taxi, and if so, it arms the taxi redirect
func (e *TaxiBot) handleMessageReactionAdd(s *discordgo.Session, reaction *discordgo.MessageReactionAdd) {
	var err error
	// ignore own reactions, and non-taxi reactions
	if reaction.UserID == s.State.User.ID || reaction.Emoji.Name != TaxiEmoji {
		return
	}

	// Search for a match token in the tracked messages
	// See if this is a tracked message
	t := TrackID{
		ChannelID: reaction.ChannelID,
		MessageID: reaction.MessageID,
	}

	matchID, found := e.linkRegistry.load(t)
	if !found {
		return
	}

	logger := e.logger.WithField("func", "handleMessageReactionAdd").WithField("reaction", reaction)

	// Check if the match is live
	if _, err = e.nk.MatchGet(e.ctx, matchID.String()); err != nil {
		logger.Warn("Error getting match: %s", err.Error())
		err := e.linkRegistry.Remove(t)
		if err != nil {
			logger.Warn("Error clearing match: %s", err.Error())
		}
		return
	}

	// Ensure the match is tracked
	e.linkRegistry.Track(matchID, reaction.ChannelID, reaction.MessageID)

	// Clear the reactions (except for the bot)
	if err = e.linkRegistry.Clear(reaction.ChannelID, reaction.MessageID, false); err != nil {
		logger.Warn("Error clearing taxi reactions: %v", err)
	}

	// Hail the taxi
	err = e.Hail(logger, reaction.UserID, matchID)
	if err != nil {
		logger.Warn("Error adding hail: %s", err.Error())
		return
	}

	// Get the user's DM channel
	dmChannelID, found := e.userChannels.Load(reaction.UserID)
	if !found {
		st, err := s.UserChannelCreate(reaction.UserID)
		if err != nil {
			logger.Warn("Error creating DM channel: %s", err.Error())
			return
		}
		if st == nil {
			return
		}
		dmChannelID = st.ID
		e.userChannels.Store(reaction.UserID, dmChannelID)
	}

	// Message the user
	// Create an echo taxi link for the message
	applink := fmt.Sprintf("<%sspark://c/%s>", EchoTaxiPrefix, strings.ToUpper(matchID.UUID().String()))
	dmMessage, err := s.ChannelMessageSend(dmChannelID, fmt.Sprintf("You have hailed a taxi to %s.\n\nGo into the game and click 'Play' on the main menu, or 'Find Match' on the lobby terminal. ", applink))
	if err != nil {
		logger.Warn("Error sending message: %v", err)
	}
	if dmMessage == nil {
		return
	}
	// React to the message
	if err = s.MessageReactionAdd(dmChannelID, dmMessage.ID, TaxiEmoji); err != nil {
		logger.Warn("Error reacting to message: %v", err)
	}
	// track the DM message
	e.linkRegistry.Track(matchID, dmChannelID, dmMessage.ID)

	e.logger.Debug("%s hailed a taxi to %s", reaction.UserID, matchID.String())
}

func (e *TaxiBot) handleMessageReactionRemove(s *discordgo.Session, reaction *discordgo.MessageReactionRemove) {
	if reaction.UserID == s.State.User.ID || reaction.Emoji.Name != TaxiEmoji {
		// ignore dm reactions and reactions from the bot
		return
	}

	// If the reaction is a taxi, remove the hail for the user
	userID, _, _, err := e.nk.AuthenticateCustom(e.ctx, reaction.UserID, "", true)
	if err != nil {
		e.logger.Warn("Error removing hail: %s", err.Error())
		return
	}
	// Remove the hail
	e.Hail(e.logger, userID, NilMatchID)

	// Clear any non-bot taxi reactions
	if err := e.linkRegistry.Clear(reaction.ChannelID, reaction.MessageID, false); err != nil {
		e.logger.Warn("Error clearing taxi reactions: %v", err)
	}
}

type EchoTaxiHailRPCRequest struct {
	UserID  string  `json:"user_id"`
	MatchID MatchID `json:"match_token"`
}

type EchoTaxiHailRPCResponse struct {
	UserID  string        `json:"user_id"`
	MatchID MatchID       `json:"match_token"`
	Label   EvrMatchState `json:"label"`
}

func (r *EchoTaxiHailRPCResponse) String() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func (e *TaxiBot) EchoTaxiHailRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	request := &EchoTaxiHailRPCRequest{}
	err := json.Unmarshal([]byte(payload), request)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error unmarshalling payload: %s", err.Error()), StatusInvalidArgument)
	}

	matchID := request.MatchID
	userID := request.UserID

	response := &EchoTaxiHailRPCResponse{
		UserID:  request.UserID,
		MatchID: matchID,
	}

	// If the MatchID is blank, remove the hail
	if matchID.IsNil() {
		// Delete the hail
		err = e.Hail(logger, userID, NilMatchID)
		if err != nil {
			return "", runtime.NewError(fmt.Sprintf("Error removing hail: %s", err.Error()), StatusInternalError)
		}
		response.MatchID = NilMatchID

		return response.String(), nil
	}

	if !matchID.IsValid() {
		return "", runtime.NewError(fmt.Sprintf("Invalid MatchID: %s", matchID), StatusInvalidArgument)
	}

	match, err := nk.MatchGet(ctx, matchID.String())
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error getting match: %s", err.Error()), StatusInternalError)
	}

	if match == nil {
		return "", runtime.NewError(fmt.Sprintf("Match not found: %s", matchID), StatusNotFound)
	}
	label := EvrMatchState{}
	err = json.Unmarshal([]byte(match.GetLabel().Value), &label)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error unmarshalling label: %s", err.Error()), StatusInternalError)
	}

	err = e.Hail(logger, userID, matchID)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error adding hail: %s", err.Error()), StatusInternalError)
	}
	response.Label = label

	return response.String(), nil
}

/*
func matchDetails(ctx context.Context, s *discordgo.Session, nk runtime.NakamaModule, logger runtime.Logger) {
	embedMap := make(map[string]*discordgo.Message)
	// Get the matches

	go func() {
		matches, err := nk.MatchList(ctx, 100, true, "", lo.ToPtr(2), lo.ToPtr(MatchMaxSize), "*")
		if err != nil {
			logger.Warn("Error getting matches: %s", err.Error())
			return
		}
		for _, match := range matches {
			// Get the match
			st, err = matchStatusEmbed(ctx, s, nk, logger, "1102748367949418620", match.MatchID)
			if err != nil {
				logger.Warn("Error getting match status: %s", err.Error())
			}

			st, found := embedMap[match.MatchID]
			if found {
				// If the match is already in the map, then update the status
				_ = st
				// Send an update to the embed
				_, err = s.ChannelMessageEditEmbed("1102748367949418620", st.ID, st, st.Embeds[0])
				if err != nil {
					logger.Warn("Error editing message: %s", err.Error())
				}
			} else {

				// Creating the message
				st, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
					Content:         "",
					TTS:             false,
					Components:      components,
					Embeds:          []*discordgo.MessageEmbed{&embed},
					AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeUsers}},
				})

				if err != nil {
					// Handle the error
					println("Error sending message:", err)
				}
				embedMap[match.MatchID] = st

			}
		}

	}()

}

func matchStatusEmbed(ctx context.Context, s *discordgo.Session, nk runtime.NakamaModule, logger runtime.Logger, channelID string, MatchID string) (*discordgo.Message, []discordgo.MessageComponent, error) {

	// Get the match
	match, err := nk.MatchGet(ctx, MatchID)
	if err != nil {
		return err
	}

	signal := EvrSignal{
		Signal: SignalGetPresences,
		Data:   []byte{},
	}
	signalJson, err := json.Marshal(signal)
	if err != nil {
		logger.Warn("Error marshalling signal: %s", err.Error())

	}
	// Signal the match to get the presences
	data, err := nk.MatchSignal(ctx, MatchID, string(signalJson))
	if err != nil {
		return err
	}
	presences := make([]*EvrMatchPresence, 0, MatchMaxSize)
	err = json.Unmarshal([]byte(data), &presences)
	if err != nil {
		return err
	}

	// Get the LAbel for
	MatchID = match.MatchID[:strings.LastIndex(match.MatchID, ".")]
	sparkLink := "https://echo.taxi/spark://c/" + strings.ToUpper(MatchID)

	// Unmarshal the label
	label := &matchState{}
	err = json.Unmarshal([]byte(match.GetLabel().Value), label)
	if err != nil {
		return err
	}

	// Get the guild
	guild, err := s.Guild(label.GuildID)
	if err != nil {
		return err
	}
	guildID := guild.ID
	guildName := guild.Name
	serverLocation := label.Broadcaster.IPinfo.Location

	gameType := ""
	switch label.LobbyType {
	case LobbyType(evr.ModeSocialPublic):
		gameType = "Public Social Lobby"
	case LobbyType(evr.ModeSocialPrivate):
		gameType = "Private Social Lobby"
	case LobbyType(evr.ModeCombatPrivate):
		gameType = "Private Combat Match"
	case LobbyType(evr.ModeCombatPublic):
		gameType = "Public Combat Match"
	case LobbyType(evr.ModeArenaPrivate):
		gameType = "Private Arena Match"
	case LobbyType(evr.ModeArenaPublic):
		gameType = "Public Arena Match"
	}

	// Create a comma-delimited list of the players by their discordIds
	players := make([]string, 0, len(presences))
	for _, p := range presences {
		// Get the user
		s := "<@" + p.DiscordID + ">"
		players = append(players, s)
	}
	// put the blue players on the left of a :small_orange_diamond:
	// put the orange players on the right of a :small_orange_diamond:
	bluePlayers := make([]string, 0)
	orangePlayers := make([]string, 0)

	for _, p := range presences {
		if p.TeamIndex == 0 {
			bluePlayers = append(bluePlayers, "<@"+p.DiscordID+">")
		} else if p.TeamIndex == 1 {
			orangePlayers = append(orangePlayers, "<@"+p.DiscordID+">")
		}
	}

	playersList := strings.Join(bluePlayers, ", ") + " :small_blue_diamond: :small_orange_diamond: " + strings.Join(orangePlayers, ", ")

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Style: discordgo.LinkButton,
					Label: "Spark",
					URL:   sparkLink,
				},
				discordgo.Button{
					Style:    discordgo.PrimaryButton,
					Label:    "EchoTaxi",
					CustomID: "row_0_button_1",
					Emoji: &discordgo.ComponentEmoji{
						Name: TaxiEmoji,
					},
				},
				discordgo.SelectMenu{
					CustomID:    "row_0_select_2",
					Placeholder: "Select Team",
					Options: []discordgo.SelectMenuOption{
						{
							Label:       "Blue",
							Value:       "Orange",
							Description: "Spectator",
						},
					},
					MinValues: lo.ToPtr(1),
					MaxValues: 1,
				},
			},
		},
	}

	// Constructing the embed
	embed := discordgo.MessageEmbed{
		Type:        "rich",
		Title:       gameType,
		Description: serverLocation,
		Color:       0x0d8b8b,
		Author: &discordgo.MessageEmbedAuthor{
			Name: guildName,
			URL:  "https://discord.com/channels/" + guildID,
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: serverLocation + " " + playersList,
		},
		URL: sparkLink,
	}

	return embeds, components, nil
}
*/
