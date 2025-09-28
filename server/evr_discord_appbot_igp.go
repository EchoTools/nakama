package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/atomic"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/thriftrw/ptr"
)

var ErrNotInAMatch = errors.New("not in a match")

var InGamePanelTimeout = 3 * time.Minute
var InGamePanelRefreshInterval = 3 * time.Second
var InGamePanelRecentPlayerTTL = 3 * time.Minute

type InGamePanel struct {
	sync.Mutex
	ctx      context.Context
	cancelFn context.CancelFunc
	nk       runtime.NakamaModule
	logger   runtime.Logger
	cache    *DiscordIntegrator
	dg       *discordgo.Session

	userID            string
	interactionCreate *atomic.Pointer[discordgo.InteractionCreate]

	currentMatchLabel *MatchLabel
	recentPlayers     []PlayerInfo
	selectedPlayer    *atomic.Pointer[PlayerInfo]
	isStopped         *atomic.Bool
}

func NewInGamePanel(nk runtime.NakamaModule, logger runtime.Logger, dg *discordgo.Session, cache *DiscordIntegrator, userID string) *InGamePanel {

	ctx, cancel := context.WithCancel(context.Background())
	logger = logger.WithFields(map[string]any{"uid": userID})
	return &InGamePanel{
		ctx:               ctx,
		cancelFn:          cancel,
		nk:                nk,
		logger:            logger,
		dg:                dg,
		cache:             cache,
		userID:            userID,
		interactionCreate: atomic.NewPointer[discordgo.InteractionCreate](nil),
		isStopped:         atomic.NewBool(false),
		selectedPlayer:    atomic.NewPointer[PlayerInfo](nil),
	}
}

func (p *InGamePanel) Stop() {
	if p == nil {
		return
	}
	p.cancelFn()
	if p.dg.InteractionResponseDelete(p.Interaction()) != nil {
		p.logger.WithField("error", "failed to delete interaction").Warn("Failed to delete interaction")
	}
	p.isStopped.Store(true)
}

func (p *InGamePanel) IsStopped() bool {
	return p.isStopped.Load()
}

func (p *InGamePanel) Swap(i *discordgo.InteractionCreate) *discordgo.InteractionCreate {
	return p.interactionCreate.Swap(i)
}

func (p *InGamePanel) Interaction() *discordgo.Interaction {
	return p.interactionCreate.Load().Interaction
}

func (p *InGamePanel) GuildID() string {
	return p.interactionCreate.Load().GuildID
}

func (p *InGamePanel) UserID() string {
	return p.userID
}

func (p *InGamePanel) retrieveActiveLoginSession(userID string) (Session, error) {
	presences, err := p.nk.StreamUserList(StreamModeService, userID, "", StreamLabelLoginService, false, true)
	if err != nil {
		return nil, fmt.Errorf("failed to list igp users: %w", err)
	}
	for _, presence := range presences {
		if presence.GetUserId() == userID {
			sessionID := uuid.FromStringOrNil(presence.GetSessionId())
			return p.nk.(*RuntimeGoNakamaModule).sessionRegistry.Get(sessionID), nil
		}
	}
	return nil, ErrSessionNotFound
}

func (p *InGamePanel) displayErrorMessage(err error) (*discordgo.Message, error) {
	return p.displayMessage(fmt.Sprintf("Error: %s", err.Error()))
}

func (p *InGamePanel) displayMessage(content string) (*discordgo.Message, error) {
	embeds := make([]*discordgo.MessageEmbed, 0)
	components := make([]discordgo.MessageComponent, 0)
	return p.dg.InteractionResponseEdit(p.Interaction(), &discordgo.WebhookEdit{
		Content:    ptr.String(content),
		Components: &components,
		Embeds:     &embeds,
	})
}

func (p *InGamePanel) retrieveCurrentMatchLabel(ctx context.Context, userID string) (*MatchLabel, error) {
	// Get the list of players in the match
	presences, err := p.nk.StreamUserList(StreamModeService, userID, "", StreamLabelMatchService, false, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get stream presences: %w", err)
	}

	if len(presences) == 0 {
		return nil, ErrNotInAMatch
	}

	// Get the match label
	label, err := MatchLabelByID(ctx, p.nk, MatchIDFromStringOrNil(presences[0].GetStatus()))
	if err != nil {
		return nil, fmt.Errorf("failed to get match label: %w", err)
	} else if label == nil {
		return nil, errors.New("failed to get match label")
	}

	return label, nil
}
func (p *InGamePanel) NewInteraction(i *discordgo.InteractionCreate) (*discordgo.InteractionCreate, error) {
	prevInteraction := p.interactionCreate.Swap(i)
	// Send the initial interaction response saying "finding session"
	return prevInteraction, dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: "Loading IGP...",
		},
	})
}

func (p *InGamePanel) Start() {
	defer p.Stop()
	var (
		logger  = p.logger
		guildID = p.GuildID()
	)

	timeoutTimer := time.NewTimer(InGamePanelTimeout)
	defer timeoutTimer.Stop()

	p.displayMessage(fmt.Sprintf("Waiting for player session... (timeout <t:%d:R>)", time.Now().Add(3*time.Minute).UTC().Unix()))
	// Find the active session for the user
SessionLoop:
	for {
		select {
		case <-p.ctx.Done():
			// If the context is done, stop the loop
			return
		case <-timeoutTimer.C:
			// If the timeout timer expires, send a message and return
			if _, err := p.displayMessage("Session timed out. Please reopen the IGP."); err != nil {
				logger.WithField("error", err).Error("Failed to send session timeout message")
				return
			}
			return
		default:
		}

		session, err := p.retrieveActiveLoginSession(p.userID)
		if err != nil {
			if errors.Is(err, ErrSessionNotFound) {
				// If the session is not found, wait for a bit and try again
				<-time.After(InGamePanelRefreshInterval)
				continue SessionLoop
			}
			if _, err := p.displayErrorMessage(err); err != nil {
				logger.WithField("error", err).Error("Failed to send error message")
				return
			}
			return
		}
		sessionCtx := session.Context()
		params, ok := LoadParams(sessionCtx)
		if !ok {
			logger.Error("Failed to load params from session context")
			return
		}

		params.isIGPOpen.Store(true)

		timeoutTimer.Reset(InGamePanelTimeout)

		content := fmt.Sprintf("Session found! Waiting for player to join a match... (timeout <t:%d:R>)", time.Now().Add(3*time.Minute).UTC().Unix())
		if _, err := p.displayMessage(content); err != nil {
			logger.WithField("error", err).Error("Failed to send message")
			return
		}

		type previousPlayer struct {
			PlayerInfo PlayerInfo
			LastSeen   time.Time
		}
		recentPlayerMap := make(map[string]previousPlayer)
		// If the session is found, get the players current match
		prevPlayerUserIDSet := make(map[string]bool)
		prevInteraction := p.Interaction()
	MatchLoop:
		for {
			select {
			case <-p.ctx.Done():
				// If the context is done, stop the loop
				return
			case <-sessionCtx.Done():
				// If the session is closed, try to find a new session
				if _, err := p.displayMessage("Waiting for player session..."); err != nil {
					logger.WithField("error", err).Error("Failed to send message")
					return
				}
				continue SessionLoop
			case <-timeoutTimer.C:
				// If the timeout timer expires, send a message and return
				if _, err := p.displayMessage("Session timed out. Please try again."); err != nil {
					logger.WithField("error", err).Error("Failed to send match timeout message")
					return
				}
				return
			default:
			}

			// Get the match label
			label, err := p.retrieveCurrentMatchLabel(sessionCtx, p.userID)
			if err != nil {
				if errors.Is(err, ErrNotInAMatch) {
					// If the user is not in a match, wait for a bit and try again
					<-time.After(InGamePanelRefreshInterval)
					continue MatchLoop
				}
				if _, err := p.displayErrorMessage(err); err != nil {
					logger.WithField("error", err).Error("Failed to send error message")
					return
				}
				return
			}
			timeoutTimer.Reset(InGamePanelTimeout)

			p.Lock()
			p.currentMatchLabel = label
			p.Unlock()

			// Prune the recent players map
			for userID, p := range recentPlayerMap {
				if time.Since(p.LastSeen) > InGamePanelRecentPlayerTTL {
					delete(recentPlayerMap, userID)
				}
			}

			latestPlayerUserIDSet := make(map[string]bool)
			for _, p := range label.Players {
				latestPlayerUserIDSet[p.UserID] = true

				if _, ok := recentPlayerMap[p.UserID]; !ok {
					recentPlayerMap[p.UserID] = previousPlayer{
						PlayerInfo: p,
						LastSeen:   time.Now(),
					}
				}
			}

			recentPlayers := make([]PlayerInfo, 0, len(recentPlayerMap))
			for _, p := range recentPlayerMap {
				recentPlayers = append(recentPlayers, p.PlayerInfo)
			}

			// Sort the recent players by last seen time
			sort.Slice(recentPlayers, func(i, j int) bool {
				lastSeenA := recentPlayerMap[recentPlayers[i].UserID].LastSeen
				lastSeenB := recentPlayerMap[recentPlayers[j].UserID].LastSeen
				return lastSeenA.After(lastSeenB)
			})

			p.Lock()
			p.recentPlayers = recentPlayers
			p.Unlock()
			interaction := p.Interaction()
			// Check if the player user ID set has changed
			if interaction != prevInteraction || !maps.Equal(prevPlayerUserIDSet, latestPlayerUserIDSet) {
				prevInteraction = interaction
				// Update the mode panel
				webHookEdit := p.createUpdateEdit(guildID, label, recentPlayers)
				if _, err := p.dg.InteractionResponseEdit(p.Interaction(), webHookEdit); err != nil {
					logger.WithField("error", err).Error("Failed to edit message")
					return
				}
			}

			// Update the player user ID set
			prevPlayerUserIDSet = latestPlayerUserIDSet

			// Wait for a bit before checking again
			<-time.After(InGamePanelRefreshInterval)
		}
	}
}

func (p *InGamePanel) createUpdateEdit(guildID string, label *MatchLabel, recentPlayers []PlayerInfo) *discordgo.WebhookEdit {

	playerNames := make([]discordgo.SelectMenuOption, 0)

	playerSet := make(map[string]bool)
	for _, p := range label.Players {
		emoji := "⚫"
		switch p.Team {
		case BlueTeam:
			emoji = "🔵"
		case OrangeTeam:
			emoji = "🟠"
		case SocialLobbyParticipant:
			emoji = "🟣"
		}

		playerNames = append(playerNames, discordgo.SelectMenuOption{
			Label:       p.DisplayName,
			Value:       p.UserID,
			Description: fmt.Sprintf("%s / %s", p.Username, p.EvrID.String()),
			Emoji:       &discordgo.ComponentEmoji{Name: emoji},
		})

		playerSet[p.UserID] = true
	}

	// Add any previous players that are not in the current match
	for _, p := range recentPlayers {
		if _, ok := playerSet[p.UserID]; !ok {
			playerNames = append(playerNames, discordgo.SelectMenuOption{
				Label:       p.DisplayName,
				Value:       p.UserID,
				Description: fmt.Sprintf("%s / %s", p.Username, p.EvrID.String()),
				Emoji:       &discordgo.ComponentEmoji{Name: "⚪"},
			})
		}
	}
	var (
		selected      = p.selectedPlayer.Load()
		defaultValues []discordgo.SelectMenuDefaultValue
		components    = make([]discordgo.MessageComponent, 0, 2)
	)
	if selected != nil {
		defaultValues = []discordgo.SelectMenuDefaultValue{
			{
				ID: selected.UserID,
			},
		}
	}

	components = append(components,
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Set IGN",
					Style:    discordgo.SecondaryButton,
					CustomID: "igp:" + p.userID + ":set_ign",
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					MenuType:      discordgo.StringSelectMenu,
					CustomID:      "igp:" + p.userID + ":select_player",
					Placeholder:   "<select a player to moderate>",
					Options:       playerNames,
					DefaultValues: defaultValues,
				},
			},
		})

	content := fmt.Sprintf("Currently in [%s](https://echo.taxi/spark://c/%s)", label.Mode.String(), strings.ToUpper(label.ID.UUID.String()))

	return &discordgo.WebhookEdit{
		Content:    ptr.String(content),
		Embeds:     p.createPlayerListEmbeds(guildID, recentPlayers),
		Components: &components,
	}
}

func (igp *InGamePanel) createPlayerListEmbeds(guildID string, players []PlayerInfo) *[]*discordgo.MessageEmbed {
	embeds := make([]*discordgo.MessageEmbed, 0, len(players))

	for _, p := range players {
		member, err := igp.cache.GuildMember(guildID, p.DiscordID)
		if err != nil {
			igp.logger.WithField("error", err).Warn("Failed to get guild member")
			continue
		}
		color := 0xFFFFFFF // default color
		switch p.Team {
		case BlueTeam:
			color = 0x0000FF // blue
		case OrangeTeam:
			color = 0xFFA500 // orange
		case SocialLobbyParticipant:
			color = 0x800080 // purple
		case Spectator:
			color = 0x808080 // gray
		case Moderator:
			color = 0xFFFF00 // yellow
		}

		embeds = append(embeds, &discordgo.MessageEmbed{
			Author: &discordgo.MessageEmbedAuthor{
				Name:    member.User.Username,
				IconURL: member.User.AvatarURL(""),
			},
			Title: p.DisplayName,
			Color: color,
		})
	}

	return &embeds
}

/*
	playerNames := make([]discordgo.SelectMenuOption, 0)

	playerSet := make(map[string]bool)
	for _, p := range label.Players {
		emoji := "⚫"
		switch p.Team {
		case BlueTeam:
			emoji = "🔵"
		case OrangeTeam:
			emoji = "🟠"
		case SocialLobbyParticipant:
			emoji = "🟣"
		}

		playerNames = append(playerNames, discordgo.SelectMenuOption{
			Label:       p.DisplayName,
			Value:       p.UserID,
			Description: fmt.Sprintf("%s / %s", p.Username, p.EvrID.String()),
			Emoji:       &discordgo.ComponentEmoji{Name: emoji},
		})

		playerSet[p.UserID] = true
	}

	// Add any previous players that are not in the current match
	for _, p := range previousPlayers {
		if _, ok := playerSet[p.UserID]; !ok {
			playerNames = append(playerNames, discordgo.SelectMenuOption{
				Label:       p.DisplayName,
				Value:       p.UserID,
				Description: fmt.Sprintf("%s / %s", p.Username, p.EvrID.String()),
				Emoji:       &discordgo.ComponentEmoji{Name: "⚪"},
			})
		}
	}

	return &[]discordgo.MessageComponent{

		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    "select",
					Placeholder: "<select a player to kick>",
					Options:     playerNames,
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    "trigger_cv",
					Placeholder: "Send user through Community Values?",
					Options: []discordgo.SelectMenuOption{
						{
							Label: "Yes",
							Value: "yes",
						},
						{
							Label: "No",
							Value: "no",
						},
					},
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    "reason",
					Placeholder: "<select a reason>",
					Options: []discordgo.SelectMenuOption{
						{
							Label: "Inappopriate behaviour",
							Value: "inappropriate_behaviour",
						},
						{
							Label: "Bullying / insulting behaviour",
							Value: "bullying_insulting_behaviour",
						},
						{
							Label: "Unsportsmanlike behaviour",
							Value: "unsportsmanlike_behaviour",
						},
						{
							Label: "General toxicity",
							Value: "general_toxicity",
						},
					},
				},
			},
		},
*/

func (p *InGamePanel) createSuspendPlayerModal(targetDiscordID, displayName string) *discordgo.InteractionResponse {
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "igp:kick_player_modal:" + targetDiscordID,
			Title:    "Moderate " + displayName,
			Content:  "Enter the duration and reason for suspension, or leave blank to kick only.",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "duration_input",
							Label:       "Duration (15m,1h,1d,1w); 0 to void existing.",
							Placeholder: "Enter suspension duration",
							Style:       discordgo.TextInputShort,
							Required:    false,
						},
					},
				},
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "user_note_input",
							Label:       "Reason (user visible)",
							Style:       discordgo.TextInputShort,
							Placeholder: `Enter reason for suspension`,
							Required:    true,
							MaxLength:   45,
						},
					},
				},
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "mod_note_input",
							Label:       "Notes (mod only)",
							Style:       discordgo.TextInputParagraph,
							Placeholder: `Enter notes for moderation team`,
							Required:    false,
							MaxLength:   200,
						},
					},
				},
			},
		},
	}
}

func (p *InGamePanel) createSetIGNModal(currentDisplayName string) *discordgo.InteractionResponse {
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "igp:set_ign_modal",
			Title:    "Set IGN",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID: "display_name_input",
							Label:    "Display Name",
							Value:    currentDisplayName,
							Style:    discordgo.TextInputShort,
							Required: true,
						},
					},
				},
			},
		},
	}
}

func (p *InGamePanel) HandleInteraction(i *discordgo.InteractionCreate, command string) error {
	if p.IsStopped() {
		return fmt.Errorf("panel is stopped")
	}

	action, value, _ := strings.Cut(command, ":")

	data := i.Interaction.MessageComponentData()

	switch action {
	case "set_ign":

		userID := p.cache.DiscordIDToUserID(i.Member.User.ID)
		if userID == "" {
			return fmt.Errorf("failed to get user ID")
		}
		groupID := p.cache.GuildIDToGroupID(i.GuildID)
		if groupID == "" {
			return fmt.Errorf("failed to get group ID")
		}

		var evrProfile *EVRProfile
		if a, err := p.nk.AccountGetId(p.ctx, userID); err != nil {
			return fmt.Errorf("failed to get account by ID: %w", err)
		} else if a.GetDisableTime() != nil {
			return fmt.Errorf("account is disabled")
		} else if evrProfile, err = BuildEVRProfileFromAccount(a); err != nil {
			return fmt.Errorf("failed to get account by ID: %w", err)
		}

		// Get the selected user ID
		modal := p.createSetIGNModal(evrProfile.GetGroupIGN(groupID))
		return p.dg.InteractionRespond(i.Interaction, modal)

	case "select_player":

		// Get the selected user ID
		selectedUserID := data.Values[0]
		var player PlayerInfo
		p.Lock()
		recentPlayers := p.recentPlayers
		p.Unlock()

		for _, p := range recentPlayers {
			if p.UserID == selectedUserID {
				player = p
				break
			}
		}
		if player.UserID == "" {
			return fmt.Errorf("no player found")
		}
		p.selectedPlayer.Store(&player)
		modal := p.createSuspendPlayerModal(player.DiscordID, player.DisplayName)
		return p.dg.InteractionRespond(i.Interaction, modal)

	case "kick_player_modal":
		p.logger.Warn("Will kick " + value)
		return nil

	default:
		return fmt.Errorf("unknown action: %s", action)

	}

}

func (d *DiscordAppBot) handleInGamePanelInteraction(i *discordgo.InteractionCreate, value string) error {

	// Split off the userID and command
	userID, command, _ := strings.Cut(value, ":")

	// Load the existing InGamePanel instance for the user
	// If it doesn't exist, create a new one and replace this interaction
	igp, ok := d.igpRegistry.Load(userID)
	if !ok {
		return fmt.Errorf("no active panel found")
	}

	return igp.HandleInteraction(i, command)
}

func (d *DiscordAppBot) handleModalSubmit(logger runtime.Logger, i *discordgo.InteractionCreate, value string) error {

	action, value, _ := strings.Cut(value, ":")

	data := i.Interaction.ModalSubmitData()

	switch action {
	case "kick_player_modal":
		// Get the selected user ID

		caller := i.Member
		target, err := d.dg.User(value)
		if err != nil {
			return fmt.Errorf("failed to get user: %w", err)
		}

		duration := data.Components[0].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value
		userNotice := data.Components[1].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value
		notes := data.Components[2].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value

		return d.kickPlayer(logger, i, caller, target, duration, userNotice, notes, false, false)
	default:
		return fmt.Errorf("unknown action: %s", action)
	}
}

func (d *DiscordAppBot) handleInGamePanel(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

	var (
		igp *InGamePanel
		ok  bool
	)

	if igp, ok = d.igpRegistry.Load(userID); ok {
		igp.Stop()
	}

	// Check if there is already a panel for this user, delete it and move to this interaction
	// Create a new InGamePanel instance
	igp = NewInGamePanel(d.nk, logger, s, d.cache, userID)

	// Start a goroutine to handle the interaction, and keep it updated
	go igp.Start()

	d.igpRegistry.Store(userID, igp)
	go func() {
		<-igp.ctx.Done()
		d.igpRegistry.Delete(userID)
	}()

	if prevInteraction, err := igp.NewInteraction(i); err != nil {
		return fmt.Errorf("failed to create new interaction: %w", err)
	} else if prevInteraction != nil {
		// If there is a previous interaction, delete it
		if err := d.dg.InteractionResponseDelete(prevInteraction.Interaction); err != nil {
			logger.WithField("error", err).Error("Failed to delete previous interaction")
		}
	}

	return nil
}
