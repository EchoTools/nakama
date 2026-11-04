package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
)

// The Discord side of the new-player ambassador commands. The logic and the
// logging live in ambassadorService; this file turns an interaction into a
// caller and a target, runs the service, and turns the outcome into the reply.

const (
	cmdSetAmbassador    = "set-ambassador"
	cmdAmbassadorRemove = "ambassador-remove"
	cmdAmbassadorToggle = "ambassador-toggle"
	cmdAmbassadorReady  = "ambassador-ready"
	cmdLevelSelfPromote = "level-self-promote"
	cmdLevelDemote      = "level-demote"
)

// ambassadorCommandNames lists every command this feature registers.
var ambassadorCommandNames = []string{
	cmdSetAmbassador, cmdAmbassadorRemove, cmdAmbassadorToggle,
	cmdAmbassadorReady, cmdLevelSelfPromote, cmdLevelDemote,
}

// ambassadorSlashCommands are appended to mainSlashCommands. Registration only
// hides nothing: the server decides who may run each one.
func ambassadorSlashCommands() []*discordgo.ApplicationCommand {
	userIDOption := func(desc string) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "user_id",
			Description: desc,
			Required:    true,
			MinLength:   &discordUserIDMinLen,
			MaxLength:   discordUserIDMaxLen,
		}
	}
	return []*discordgo.ApplicationCommand{
		{
			Name:        cmdSetAmbassador,
			Description: "Moderators: make a player an ambassador.",
			Options: []*discordgo.ApplicationCommandOption{{
				Type:        discordgo.ApplicationCommandOptionUser,
				Name:        "user",
				Description: "The player to make an ambassador.",
				Required:    true,
			}},
		},
		{
			Name:        cmdAmbassadorRemove,
			Description: "Moderators: remove a player's ambassador role. They are told why.",
			Options: []*discordgo.ApplicationCommandOption{
				userIDOption("The ambassador's Discord user ID (the number)."),
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reason",
					Description: "Why. The ambassador is sent this.",
					Required:    true,
					MaxLength:   AmbassadorRemovalReasonMaxLen,
				},
			},
		},
		{
			Name:        cmdAmbassadorToggle,
			Description: "Ambassadors: switch between normal matchmaking and ambassador mode.",
		},
		{
			Name:        cmdAmbassadorReady,
			Description: "Ambassadors: mark a new player ready and raise their matchmaking level.",
			Options: []*discordgo.ApplicationCommandOption{
				userIDOption("The player's Discord user ID (the number)."),
			},
		},
		{
			Name:        cmdLevelSelfPromote,
			Description: "Raise your own matchmaking level.",
		},
		{
			Name:        cmdLevelDemote,
			Description: "Moderators: lower a player's matchmaking level. They are told.",
			Options: []*discordgo.ApplicationCommandOption{
				userIDOption("The player's Discord user ID (the number)."),
			},
		},
	}
}

// Discord snowflakes are 17 to 20 digits. A typed ID outside that is a mistake,
// not a player.
const (
	discordUserIDMaxLen = 20
)

var discordUserIDMinLen = 17

// parseDiscordUserID accepts only a Discord user ID typed as digits. A name, a
// mention or a number of the wrong length is refused: the ID is typed on
// purpose so nobody is moved by accident.
func parseDiscordUserID(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) < discordUserIDMinLen || len(s) > discordUserIDMaxLen {
		return "", false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return s, true
}

// ambassadorStaffCapacity is a caller's staff capacity for the guild a command
// ran in: a global operator, or an enforcer of that guild. Auditors are not
// staff for these commands. Anything else is no capacity.
func ambassadorStaffCapacity(isGlobalOperator bool, gg *GuildGroup, userID string) ambassadorCapacity {
	if isGlobalOperator {
		return capacityOperator
	}
	if gg != nil && gg.State != nil && gg.IsEnforcer(userID) {
		return capacityEnforcer
	}
	return ""
}

// Replies, ephemeral. The wording is the approved replies.md.
const (
	replyWriteFailed   = "Could not save the change. Nothing was changed; try again."
	replyNotAnID       = "Enter the person's Discord user ID (the number), not their name."
	replyNotAnIDPlayer = "Enter the player's Discord user ID (the number), not their name."
	replyNoSuchPlayer  = "No linked player has that Discord ID."
	replyNotMod        = "Only moderators can use this command."
)

func mention(discordID string) string { return "<@" + discordID + ">" }

func replyForGrant(res ambassadorResult, target string) string {
	switch res.Outcome {
	case outcomeOK:
		return fmt.Sprintf("%s is now an ambassador. They start in normal matchmaking; they switch with /ambassador-toggle.", target)
	case outcomeNoop:
		return fmt.Sprintf("%s is already an ambassador. Nothing changed.", target)
	case outcomeSelfTarget:
		return "You can't grant yourself the ambassador role."
	case outcomeDenied:
		return replyNotMod
	}
	return replyWriteFailed
}

func replyForRemove(res ambassadorResult, target string) string {
	switch res.Outcome {
	case outcomeOK:
		if res.DMFailed {
			return fmt.Sprintf("%s is no longer an ambassador, but I couldn't DM them.", target)
		}
		return fmt.Sprintf("%s is no longer an ambassador and has been told why.", target)
	case outcomeNoop:
		return fmt.Sprintf("%s is not an ambassador. Nothing changed.", target)
	case outcomeSelfTarget:
		return "You can't remove your own ambassador role."
	case outcomeReasonRequired:
		return "A reason is required."
	case outcomeReasonTooLong:
		return fmt.Sprintf("The reason is too long (max %d characters).", AmbassadorRemovalReasonMaxLen)
	case outcomeDenied:
		return replyNotMod
	}
	return replyWriteFailed
}

func replyForToggle(res ambassadorResult) string {
	switch res.Outcome {
	case outcomeOK:
		if res.On {
			return "Ambassador mode is ON. Public arena and combat matches you enter now won't change your rating or stats."
		}
		return "Ambassador mode is OFF. You're back in normal matchmaking and your games count."
	case outcomeDenied:
		return "You're not an ambassador."
	}
	return replyWriteFailed
}

func replyForReady(res ambassadorResult, target string) string {
	switch res.Outcome {
	case outcomeOK:
		if res.DMFailed {
			return fmt.Sprintf("%s is marked ready, but I couldn't DM them.", target)
		}
		return fmt.Sprintf("%s is marked ready and has been told their level was raised.", target)
	case outcomeNoop:
		return fmt.Sprintf("%s is already at the raised level. Nothing changed.", target)
	case outcomeSelfTarget:
		return "You can't mark yourself ready."
	case outcomeDenied:
		return "Only ambassadors can use this command."
	}
	return replyWriteFailed
}

func replyForSelfPromote(res ambassadorResult) string {
	switch res.Outcome {
	case outcomeOK:
		if res.DMFailed {
			return "Your matchmaking level has been raised and applies from your next login. I couldn't DM you."
		}
		return "Your matchmaking level has been raised. You'll get a DM, and it applies from your next login."
	case outcomeNoop:
		return "Your level is already raised. Nothing changed."
	}
	return replyWriteFailed
}

func replyForDemote(res ambassadorResult, target string) string {
	switch res.Outcome {
	case outcomeOK:
		if res.DMFailed {
			return fmt.Sprintf("%s was moved down a tier, but I couldn't DM them.", target)
		}
		return fmt.Sprintf("%s was moved down a tier and has been told.", target)
	case outcomeNoop:
		return fmt.Sprintf("%s is not at the raised level. Nothing changed.", target)
	case outcomeSelfTarget:
		return "You can't change your own level."
	case outcomeDenied:
		return replyNotMod
	}
	return replyWriteFailed
}

// ambassadorOptions flattens a command's options to name -> typed value. A user
// option's value is the user's Discord id.
func ambassadorOptions(opts []*discordgo.ApplicationCommandInteractionDataOption) map[string]string {
	out := make(map[string]string, len(opts))
	for _, o := range opts {
		out[o.Name] = fmt.Sprint(o.Value)
	}
	return out
}

// ambassadorServiceFor builds the service for one interaction.
func (d *DiscordAppBot) ambassadorServiceFor(logger runtime.Logger) *ambassadorService {
	return newAmbassadorService(d.nk, logger, func(ctx context.Context, discordID, text string) error {
		_, err := SendUserMessage(ctx, d.dg, discordID, text)
		return err
	})
}

// ambassadorCaller works out who is calling and what staff capacity they hold
// from verified permissions: the global operator group, and the guild's
// enforcer role. Nothing comes from an option value. A failed permission lookup
// fails closed.
func (d *DiscordAppBot) ambassadorCaller(ctx context.Context, logger runtime.Logger, i *discordgo.InteractionCreate, user *discordgo.User, userID, groupID, command string, args map[string]string) ambassadorCaller {
	isOperator := false
	if perms := PermissionsFromContext(ctx); perms != nil {
		isOperator = perms.IsGlobalOperator
	} else if userID != "" {
		var err error
		if isOperator, err = CheckSystemGroupMembership(ctx, d.db, userID, GroupGlobalOperators); err != nil {
			logger.WithField("error", err).Warn("Failed to check global operator status for an ambassador command; treating as not an operator")
			isOperator = false
		}
	}
	var gg *GuildGroup
	if d.guildGroupRegistry != nil && groupID != "" {
		gg = d.guildGroupRegistry.Get(groupID)
	}
	return ambassadorCaller{
		UserID:    userID,
		DiscordID: user.ID,
		Staff:     ambassadorStaffCapacity(isOperator, gg, userID),
		GuildID:   i.GuildID,
		GroupID:   groupID,
		Command:   command,
		Args:      args,
	}
}

// ambassadorAuditPost posts the command to the guild's audit channel, the way
// kick-player does. Called on success only: denials are logged, not posted.
func (d *DiscordAppBot) ambassadorAuditPost(logger runtime.Logger, i *discordgo.InteractionCreate, groupID string) {
	if d.guildGroupRegistry == nil {
		return
	}
	gg := d.guildGroupRegistry.Get(groupID)
	if gg == nil {
		return
	}
	if err := d.LogInteractionToChannel(i, gg.AuditChannelID); err != nil {
		logger.WithField("error", err).Warn("Failed to post an ambassador command to the guild audit channel")
	}
}

// discordDisplayName is the Discord display name of a user (global name,
// falling back to username). Empty when it cannot be looked up.
func (d *DiscordAppBot) discordDisplayName(discordID string) string {
	if d.dg == nil {
		return ""
	}
	u, err := d.dg.User(discordID)
	if err != nil || u == nil {
		return ""
	}
	if u.GlobalName != "" {
		return u.GlobalName
	}
	return u.Username
}

// resolveTypedTarget turns a typed Discord user ID into a linked account. It
// replies and reports false when the input is not an ID or has no linked account.
func (d *DiscordAppBot) resolveTypedTarget(s *discordgo.Session, i *discordgo.InteractionCreate, raw string, playerWording bool) (ambassadorTarget, bool, error) {
	id, ok := parseDiscordUserID(raw)
	if !ok {
		msg := replyNotAnID
		if playerWording {
			msg = replyNotAnIDPlayer
		}
		return ambassadorTarget{}, false, editInteractionResponse(s, i, msg)
	}
	uid := d.cache.DiscordIDToUserID(id)
	if uid == "" {
		return ambassadorTarget{}, false, editInteractionResponse(s, i, replyNoSuchPlayer)
	}
	return ambassadorTarget{UserID: uid, DiscordID: id}, true, nil
}

func (d *DiscordAppBot) handleSetAmbassadorCommand(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	args := ambassadorOptions(i.ApplicationCommandData().Options)
	caller := d.ambassadorCaller(ctx, logger, i, user, userID, groupID, cmdSetAmbassador, args)
	targetDiscordID := args["user"]
	targetUID := d.cache.DiscordIDToUserID(targetDiscordID)
	if targetUID == "" {
		return editInteractionResponse(s, i, fmt.Sprintf("%s has no linked headset account.", mention(targetDiscordID)))
	}
	target := ambassadorTarget{UserID: targetUID, DiscordID: targetDiscordID}
	res, _ := d.ambassadorServiceFor(logger).Grant(ctx, caller, target)
	if res.Outcome == outcomeOK {
		d.ambassadorAuditPost(logger, i, groupID)
	}
	return editInteractionResponse(s, i, replyForGrant(res, mention(targetDiscordID)))
}

func (d *DiscordAppBot) handleAmbassadorRemoveCommand(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	args := ambassadorOptions(i.ApplicationCommandData().Options)
	caller := d.ambassadorCaller(ctx, logger, i, user, userID, groupID, cmdAmbassadorRemove, args)
	if !caller.isStaff() {
		// Refuse before resolving anything about the target.
		res, _ := d.ambassadorServiceFor(logger).Remove(ctx, caller, ambassadorTarget{DiscordID: args["user_id"]}, args["reason"])
		return editInteractionResponse(s, i, replyForRemove(res, ""))
	}
	target, ok, err := d.resolveTypedTarget(s, i, args["user_id"], false)
	if !ok {
		return err
	}
	target.DisplayName = d.discordDisplayName(target.DiscordID)
	res, _ := d.ambassadorServiceFor(logger).Remove(ctx, caller, target, args["reason"])
	if res.Outcome == outcomeOK {
		d.ambassadorAuditPost(logger, i, groupID)
	}
	return editInteractionResponse(s, i, replyForRemove(res, mention(target.DiscordID)))
}

func (d *DiscordAppBot) handleAmbassadorToggleCommand(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	caller := d.ambassadorCaller(ctx, logger, i, user, userID, groupID, cmdAmbassadorToggle, ambassadorOptions(i.ApplicationCommandData().Options))
	res, _ := d.ambassadorServiceFor(logger).Toggle(ctx, caller)
	return editInteractionResponse(s, i, replyForToggle(res))
}

func (d *DiscordAppBot) handleAmbassadorReadyCommand(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	args := ambassadorOptions(i.ApplicationCommandData().Options)
	caller := d.ambassadorCaller(ctx, logger, i, user, userID, groupID, cmdAmbassadorReady, args)
	svc := d.ambassadorServiceFor(logger)
	if isAmb, _ := svc.callerIsAmbassador(ctx, caller); !isAmb {
		// Refuse before saying anything about whether the typed ID is a linked player.
		res, _ := svc.Ready(ctx, caller, ambassadorTarget{DiscordID: args["user_id"]})
		return editInteractionResponse(s, i, replyForReady(res, ""))
	}
	target, ok, err := d.resolveTypedTarget(s, i, args["user_id"], true)
	if !ok {
		return err
	}
	res, _ := svc.Ready(ctx, caller, target)
	if res.Outcome == outcomeOK {
		d.ambassadorAuditPost(logger, i, groupID)
	}
	return editInteractionResponse(s, i, replyForReady(res, mention(target.DiscordID)))
}

func (d *DiscordAppBot) handleLevelSelfPromoteCommand(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	caller := d.ambassadorCaller(ctx, logger, i, user, userID, groupID, cmdLevelSelfPromote, ambassadorOptions(i.ApplicationCommandData().Options))
	res, _ := d.ambassadorServiceFor(logger).SelfPromote(ctx, caller)
	if res.Outcome == outcomeOK {
		d.ambassadorAuditPost(logger, i, groupID)
	}
	return editInteractionResponse(s, i, replyForSelfPromote(res))
}

func (d *DiscordAppBot) handleLevelDemoteCommand(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	args := ambassadorOptions(i.ApplicationCommandData().Options)
	caller := d.ambassadorCaller(ctx, logger, i, user, userID, groupID, cmdLevelDemote, args)
	if !caller.isStaff() {
		res, _ := d.ambassadorServiceFor(logger).Demote(ctx, caller, ambassadorTarget{DiscordID: args["user_id"]})
		return editInteractionResponse(s, i, replyForDemote(res, ""))
	}
	target, ok, err := d.resolveTypedTarget(s, i, args["user_id"], true)
	if !ok {
		return err
	}
	res, _ := d.ambassadorServiceFor(logger).Demote(ctx, caller, target)
	if res.Outcome == outcomeOK {
		d.ambassadorAuditPost(logger, i, groupID)
	}
	return editInteractionResponse(s, i, replyForDemote(res, mention(target.DiscordID)))
}
