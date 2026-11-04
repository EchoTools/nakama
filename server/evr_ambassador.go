package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/heroiclabs/nakama-common/runtime"
)

// New-player ambassadors.
//
// An ambassador is an experienced player who joins new players' matches to
// teach, not to win. A moderator grants the role; the holder switches into
// ambassador mode when they want to; in ambassador mode, and only for them,
// the stats a match would write for the ambassador are not written. An
// ambassador (or the player) can graduate a new player, which is a stored mark
// on the account that the login pool placement respects. Staff can reverse it.
//
// Nothing here touches the matchmaker: no ticket property carries any of it.

const (
	StorageCollectionAmbassador = "Ambassador"
	// StorageKeyAmbassadorAccount is deliberately not "state": the retired
	// /ambassador command stored its own object there, with an is_active flag that
	// means something else. That object is left in place and is never read.
	StorageKeyAmbassadorAccount = "account"
)

// Tier labels shown to players. "Students" is a display label only: the
// internal pool key stays "green" (see newPlayerPoolDivision). Expert belongs to
// the first-sign-in poll and is not built here.
const (
	TierFrom = "Students"
	TierTo   = "Intermediate"
)

// The org name and the sign-off in the removal DM are named constants because
// the Lounge's ambassador leads are not agreed yet and the wording may change.
const (
	AmbassadorOrgName        = "Echo VR Lounge"
	AmbassadorRemovalSignOff = "-The Ambassador Leads"
)

// AmbassadorRemovalReasonMaxLen bounds the reason a moderator types so the DM
// stays well under Discord's message limit.
const AmbassadorRemovalReasonMaxLen = 500

// The last line of a tier-change DM says who changed the tier.
const (
	tierReasonAmbassador = "You gained a tier from an ambassador."
	tierReasonSelf       = "You gained a tier by promoting yourself."
	tierReasonStaff      = "You were moved down a tier by staff."
)

// Graduation provenance recorded on the account.
const (
	graduatedViaAmbassador = "ambassador"
	graduatedViaSelf       = "self"
)

// AmbassadorAccount is a player's ambassador role, ambassador mode and
// graduated mark. Server-only. The zero value is the safe state: not an
// ambassador, normal matchmaking, not graduated.
type AmbassadorAccount struct {
	// Ambassador is the role. It means the holder MAY be an ambassador.
	Ambassador bool `json:"ambassador"`
	// Mode is ambassador mode: the holder switched into it. Effective only with the role.
	Mode bool `json:"mode"`

	GrantedBy      string    `json:"granted_by,omitempty"` // user id of the moderator
	GrantedAt      time.Time `json:"granted_at,omitempty"`
	GrantedGuildID string    `json:"granted_guild_id,omitempty"` // the guild it was granted from
	GrantedGroupID string    `json:"granted_group_id,omitempty"`

	RemovedBy      string    `json:"removed_by,omitempty"`
	RemovedAt      time.Time `json:"removed_at,omitempty"`
	RemovalReason  string    `json:"removal_reason,omitempty"`
	RemovedGuildID string    `json:"removed_guild_id,omitempty"`

	// Graduated is the account mark: the player was raised a tier. While the
	// new-player pool is off it is only a mark; once on, a graduated account is
	// kept out of the pool at login.
	Graduated      bool      `json:"graduated"`
	GraduatedBy    string    `json:"graduated_by,omitempty"` // user id of the ambassador, or of the player for "self"
	GraduatedVia   string    `json:"graduated_via,omitempty"`
	GraduatedAt    time.Time `json:"graduated_at,omitempty"`
	DemotedBy      string    `json:"demoted_by,omitempty"` // user id of the staff member
	DemotedAt      time.Time `json:"demoted_at,omitempty"`
	DemotedGuildID string    `json:"demoted_guild_id,omitempty"`

	version string
}

func (a *AmbassadorAccount) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionAmbassador,
		Key:             StorageKeyAmbassadorAccount,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         a.version,
	}
}

func (a *AmbassadorAccount) SetStorageMeta(meta StorableMetadata) {
	a.version = meta.Version
}

// EffectiveMode reports whether the holder is in ambassador mode right now:
// the role AND the mode. Removing the role turns the effect off by itself.
func (a *AmbassadorAccount) EffectiveMode() bool {
	return a != nil && a.Ambassador && a.Mode
}

func (a AmbassadorAccount) logView() map[string]any {
	return map[string]any{"ambassador": a.Ambassador, "mode": a.Mode, "graduated": a.Graduated}
}

// LoadAmbassadorAccount reads the account. A player with no stored object gets
// the zero account, written create-only ("*") if it is ever saved. It never
// creates the object on read: login and queue read this for every player.
func LoadAmbassadorAccount(ctx context.Context, nk runtime.NakamaModule, userID string) (*AmbassadorAccount, error) {
	a := &AmbassadorAccount{}
	if err := StorableRead(ctx, nk, userID, a, false); err != nil {
		if !isStorageNotFoundError(err) {
			return nil, err
		}
		a = &AmbassadorAccount{version: "*"}
	}
	return a, nil
}

// ambassadorCapacity is what the caller is acting as. Staff capacities are
// decided by the caller from verified permissions, never from an option value.
type ambassadorCapacity string

const (
	capacityOperator   ambassadorCapacity = "operator"
	capacityEnforcer   ambassadorCapacity = "enforcer"
	capacityAmbassador ambassadorCapacity = "ambassador"
	capacitySelf       ambassadorCapacity = "self"
	capacityPlayer     ambassadorCapacity = "player"
)

// ambassadorCaller is who invoked a command and with what, for the permission
// check and the log record.
type ambassadorCaller struct {
	UserID    string
	DiscordID string
	// Staff is capacityOperator or capacityEnforcer when the caller is a global
	// operator or an enforcer of the guild the command ran in; empty otherwise.
	Staff   ambassadorCapacity
	GuildID string
	GroupID string
	Command string
	Args    map[string]string // the arguments as typed
}

func (c ambassadorCaller) isStaff() bool {
	return c.Staff == capacityOperator || c.Staff == capacityEnforcer
}

// ambassadorTarget is the account a command acts on.
type ambassadorTarget struct {
	UserID      string
	DiscordID   string
	DisplayName string
}

type ambassadorOutcome string

const (
	outcomeOK             ambassadorOutcome = "ok"
	outcomeNoop           ambassadorOutcome = "noop"
	outcomeDenied         ambassadorOutcome = "denied"
	outcomeSelfTarget     ambassadorOutcome = "self_target"
	outcomeReasonRequired ambassadorOutcome = "reason_required"
	outcomeReasonTooLong  ambassadorOutcome = "reason_too_long"
	outcomeError          ambassadorOutcome = "error"
)

// ambassadorResult is what a command did. DMFailed is set when the state
// changed but the DM could not be delivered; the state change stands.
type ambassadorResult struct {
	Outcome  ambassadorOutcome
	DMFailed bool
	// On is the new ambassador mode, set by Toggle.
	On bool
}

// ambassadorService holds the logic for the five commands and the removal.
// It is the single place that changes the account and the single place that
// logs, so no command path can skip either.
type ambassadorService struct {
	nk     runtime.NakamaModule
	logger runtime.Logger
	// sendDM delivers a DM to a Discord user id.
	sendDM func(ctx context.Context, discordID, text string) error
	now    func() time.Time
}

func newAmbassadorService(nk runtime.NakamaModule, logger runtime.Logger, sendDM func(ctx context.Context, discordID, text string) error) *ambassadorService {
	return &ambassadorService{nk: nk, logger: logger, sendDM: sendDM, now: func() time.Time { return time.Now().UTC() }}
}

// ambassadorEvent is one record. Every grant, removal, toggle, ready,
// self-promotion, graduation, demotion, denial, DM result and suppressed write
// is one of these.
type ambassadorEvent struct {
	Event         string
	Caller        ambassadorCaller
	Capacity      ambassadorCapacity // what the caller acted as
	Target        ambassadorTarget
	Outcome       ambassadorOutcome
	Reason        string // why a command was denied or did nothing
	RemovalReason string // the text a moderator typed on ambassador-remove
	Before, After map[string]any
	Err           error
}

// logAmbassadorEvent is the single choke point for ambassador records: one
// structured line, message "ambassador_event", with who did it, to what, with
// the full arguments. Ids are stable user ids, never display names.
func logAmbassadorEvent(logger runtime.Logger, ev ambassadorEvent) {
	fields := map[string]any{
		"event":             ev.Event,
		"command":           ev.Caller.Command,
		"actor_user_id":     ev.Caller.UserID,
		"actor_discord_id":  ev.Caller.DiscordID,
		"actor_capacity":    string(ev.Capacity),
		"target_user_id":    ev.Target.UserID,
		"target_discord_id": ev.Target.DiscordID,
		"guild_id":          ev.Caller.GuildID,
		"group_id":          ev.Caller.GroupID,
		"args":              ev.Caller.Args,
		"outcome":           string(ev.Outcome),
	}
	if ev.Reason != "" {
		fields["reason"] = ev.Reason
	}
	if ev.RemovalReason != "" {
		fields["removal_reason"] = ev.RemovalReason
	}
	if ev.Before != nil {
		fields["before"] = ev.Before
	}
	if ev.After != nil {
		fields["after"] = ev.After
	}
	if ev.Err != nil {
		fields["error"] = ev.Err.Error()
		logger.WithFields(fields).Error("ambassador_event")
		return
	}
	logger.WithFields(fields).Info("ambassador_event")
}

func (s *ambassadorService) log(ev ambassadorEvent) { logAmbassadorEvent(s.logger, ev) }

const accountUpdateAttempts = 3

// update loads the account, applies mutate, and saves it when mutate reports a
// change. A version conflict reloads and re-applies, so a concurrent writer's
// work is not lost and the decision is made against the fresh state.
func (s *ambassadorService) update(ctx context.Context, userID string, mutate func(a *AmbassadorAccount) bool) (before, after AmbassadorAccount, changed bool, err error) {
	for attempt := 0; attempt < accountUpdateAttempts; attempt++ {
		var a *AmbassadorAccount
		a, err = LoadAmbassadorAccount(ctx, s.nk, userID)
		if err != nil {
			return before, after, false, err
		}
		before = *a
		if !mutate(a) {
			return before, *a, false, nil
		}
		err = StorableWrite(ctx, s.nk, userID, a)
		if err == nil {
			return before, *a, true, nil
		}
		if !errors.Is(err, runtime.ErrStorageRejectedVersion) {
			return before, after, false, err
		}
	}
	return before, after, false, err
}

func (s *ambassadorService) deny(c ambassadorCaller, capacity ambassadorCapacity, t ambassadorTarget, reason string) ambassadorResult {
	s.log(ambassadorEvent{Event: "denied", Caller: c, Capacity: capacity, Target: t, Outcome: outcomeDenied, Reason: reason})
	return ambassadorResult{Outcome: outcomeDenied}
}

func (s *ambassadorService) fail(c ambassadorCaller, capacity ambassadorCapacity, t ambassadorTarget, err error) (ambassadorResult, error) {
	s.log(ambassadorEvent{Event: "error", Caller: c, Capacity: capacity, Target: t, Outcome: outcomeError, Err: err})
	return ambassadorResult{Outcome: outcomeError}, err
}

// loadCallerAmbassador reads the caller's own account and reports whether they
// hold the role. The role is read from storage on every call, never trusted
// from the client.
func (s *ambassadorService) callerIsAmbassador(ctx context.Context, c ambassadorCaller) (bool, error) {
	a, err := LoadAmbassadorAccount(ctx, s.nk, c.UserID)
	if err != nil {
		return false, err
	}
	return a.Ambassador, nil
}

// Grant gives the target the ambassador role. Mods only; no self-grant. A new
// grant starts in normal matchmaking, and the target is not told.
func (s *ambassadorService) Grant(ctx context.Context, c ambassadorCaller, t ambassadorTarget) (ambassadorResult, error) {
	if !c.isStaff() {
		return s.deny(c, capacityPlayer, t, "not_staff"), nil
	}
	if c.UserID == t.UserID {
		s.log(ambassadorEvent{Event: "denied", Caller: c, Capacity: c.Staff, Target: t, Outcome: outcomeSelfTarget, Reason: "self_target"})
		return ambassadorResult{Outcome: outcomeSelfTarget}, nil
	}
	before, after, changed, err := s.update(ctx, t.UserID, func(a *AmbassadorAccount) bool {
		if a.Ambassador {
			return false
		}
		a.Ambassador = true
		a.Mode = false
		a.GrantedBy = c.UserID
		a.GrantedAt = s.now()
		a.GrantedGuildID = c.GuildID
		a.GrantedGroupID = c.GroupID
		return true
	})
	if err != nil {
		return s.fail(c, c.Staff, t, err)
	}
	if !changed {
		s.log(ambassadorEvent{Event: "noop", Caller: c, Capacity: c.Staff, Target: t, Outcome: outcomeNoop, Reason: "already_ambassador", Before: before.logView(), After: after.logView()})
		return ambassadorResult{Outcome: outcomeNoop}, nil
	}
	s.log(ambassadorEvent{Event: "grant", Caller: c, Capacity: c.Staff, Target: t, Outcome: outcomeOK, Before: before.logView(), After: after.logView()})
	return ambassadorResult{Outcome: outcomeOK}, nil
}

// removalDM is the DM an ambassador gets when a moderator removes the role.
// The user name is markdown-escaped; the reason is the moderator's text, verbatim.
func removalDM(displayName, reason string) string {
	if strings.TrimSpace(displayName) == "" {
		displayName = "there"
	}
	return fmt.Sprintf("Hi %s. Please read the following:\n"+
		"We (%s) have unfortunately decided to remove you from the ambassador team. "+
		"We are thankful for your work, but some things came up that we don't allow our ambassadors to do. "+
		"We are not trying to cause any issues, but we just want what's best for the community.\n"+
		"Reason: %s\n"+
		"Again, thank you for volunteering,\n"+
		"%s", EscapeDiscordMarkdown(displayName), AmbassadorOrgName, reason, AmbassadorRemovalSignOff)
}

// Remove takes the ambassador role away, clears ambassador mode, and tells the
// person why. Mods only; no self-target; the reason is required. People the
// ambassador graduated stay graduated. A DM failure still removes the role.
func (s *ambassadorService) Remove(ctx context.Context, c ambassadorCaller, t ambassadorTarget, reason string) (ambassadorResult, error) {
	if !c.isStaff() {
		return s.deny(c, capacityPlayer, t, "not_staff"), nil
	}
	if c.UserID == t.UserID {
		s.log(ambassadorEvent{Event: "denied", Caller: c, Capacity: c.Staff, Target: t, Outcome: outcomeSelfTarget, Reason: "self_target"})
		return ambassadorResult{Outcome: outcomeSelfTarget}, nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		s.log(ambassadorEvent{Event: "denied", Caller: c, Capacity: c.Staff, Target: t, Outcome: outcomeReasonRequired, Reason: "reason_required"})
		return ambassadorResult{Outcome: outcomeReasonRequired}, nil
	}
	if utf8.RuneCountInString(reason) > AmbassadorRemovalReasonMaxLen {
		s.log(ambassadorEvent{Event: "denied", Caller: c, Capacity: c.Staff, Target: t, Outcome: outcomeReasonTooLong, Reason: "reason_too_long"})
		return ambassadorResult{Outcome: outcomeReasonTooLong}, nil
	}
	before, after, changed, err := s.update(ctx, t.UserID, func(a *AmbassadorAccount) bool {
		if !a.Ambassador {
			return false
		}
		a.Ambassador = false
		a.Mode = false
		a.RemovedBy = c.UserID
		a.RemovedAt = s.now()
		a.RemovalReason = reason
		a.RemovedGuildID = c.GuildID
		return true
	})
	if err != nil {
		return s.fail(c, c.Staff, t, err)
	}
	if !changed {
		s.log(ambassadorEvent{Event: "noop", Caller: c, Capacity: c.Staff, Target: t, Outcome: outcomeNoop, Reason: "not_ambassador", Before: before.logView(), After: after.logView()})
		return ambassadorResult{Outcome: outcomeNoop}, nil
	}
	s.log(ambassadorEvent{Event: "remove", Caller: c, Capacity: c.Staff, Target: t, Outcome: outcomeOK, RemovalReason: reason, Before: before.logView(), After: after.logView()})
	res := ambassadorResult{Outcome: outcomeOK}
	res.DMFailed = s.deliverDM(ctx, c, c.Staff, t, removalDM(t.DisplayName, reason))
	return res, nil
}

// deliverDM sends a DM and logs the result. It reports whether delivery failed.
// A failed DM never undoes the state change that caused it.
func (s *ambassadorService) deliverDM(ctx context.Context, c ambassadorCaller, capacity ambassadorCapacity, t ambassadorTarget, text string) (failed bool) {
	if t.DiscordID == "" || s.sendDM == nil {
		s.log(ambassadorEvent{Event: "dm_failed", Caller: c, Capacity: capacity, Target: t, Outcome: outcomeError, Reason: "no_discord_id_or_sender"})
		return true
	}
	if err := s.sendDM(ctx, t.DiscordID, text); err != nil {
		s.log(ambassadorEvent{Event: "dm_failed", Caller: c, Capacity: capacity, Target: t, Outcome: outcomeError, Err: err})
		return true
	}
	s.log(ambassadorEvent{Event: "dm_sent", Caller: c, Capacity: capacity, Target: t, Outcome: outcomeOK})
	return false
}

// Toggle flips the caller between normal matchmaking and ambassador mode.
// Ambassadors only.
func (s *ambassadorService) Toggle(ctx context.Context, c ambassadorCaller) (ambassadorResult, error) {
	self := ambassadorTarget{UserID: c.UserID, DiscordID: c.DiscordID}
	ok, err := s.callerIsAmbassador(ctx, c)
	if err != nil {
		return s.fail(c, capacityAmbassador, self, err)
	}
	if !ok {
		return s.deny(c, capacityPlayer, self, "not_ambassador"), nil
	}
	before, after, _, err := s.update(ctx, c.UserID, func(a *AmbassadorAccount) bool {
		if !a.Ambassador {
			return false
		}
		a.Mode = !a.Mode
		return true
	})
	if err != nil {
		return s.fail(c, capacityAmbassador, self, err)
	}
	if !after.Ambassador {
		// The role was removed between the check and the write.
		return s.deny(c, capacityPlayer, self, "not_ambassador"), nil
	}
	event := "toggle_off"
	if after.Mode {
		event = "toggle_on"
	}
	s.log(ambassadorEvent{Event: event, Caller: c, Capacity: capacityAmbassador, Target: self, Outcome: outcomeOK, Before: before.logView(), After: after.logView()})
	return ambassadorResult{Outcome: outcomeOK, On: after.Mode}, nil
}

// tierDM is the DM for a tier change: the same text for a raise and a lowering,
// with the tiers swapped and the last line saying who did it.
func tierDM(from, to, reason string) string {
	return fmt.Sprintf("You have reached the milestone to be raised a tier in matchmaking. Please view the following:\n\n%s ---> %s\n%s", from, to, reason)
}

// graduate sets the graduated mark on the target and tells them. Already
// graduated is a no-op with no second DM. The mark takes effect on the pool at
// the target's next login.
func (s *ambassadorService) graduate(ctx context.Context, c ambassadorCaller, capacity ambassadorCapacity, event, via, tierReason string, t ambassadorTarget) (ambassadorResult, error) {
	before, after, changed, err := s.update(ctx, t.UserID, func(a *AmbassadorAccount) bool {
		if a.Graduated {
			return false
		}
		a.Graduated = true
		a.GraduatedBy = c.UserID
		a.GraduatedVia = via
		a.GraduatedAt = s.now()
		return true
	})
	if err != nil {
		return s.fail(c, capacity, t, err)
	}
	if !changed {
		s.log(ambassadorEvent{Event: "noop", Caller: c, Capacity: capacity, Target: t, Outcome: outcomeNoop, Reason: "already_graduated", Before: before.logView(), After: after.logView()})
		return ambassadorResult{Outcome: outcomeNoop}, nil
	}
	s.log(ambassadorEvent{Event: event, Caller: c, Capacity: capacity, Target: t, Outcome: outcomeOK, Before: before.logView(), After: after.logView()})
	s.log(ambassadorEvent{Event: "graduate", Caller: c, Capacity: capacity, Target: t, Outcome: outcomeOK, Before: before.logView(), After: after.logView()})
	res := ambassadorResult{Outcome: outcomeOK}
	res.DMFailed = s.deliverDM(ctx, c, capacity, t, tierDM(TierFrom, TierTo, tierReason))
	return res, nil
}

// Ready marks a player ready: graduated, by an ambassador. Ambassadors only; no
// self-target; no other requirement.
func (s *ambassadorService) Ready(ctx context.Context, c ambassadorCaller, t ambassadorTarget) (ambassadorResult, error) {
	ok, err := s.callerIsAmbassador(ctx, c)
	if err != nil {
		return s.fail(c, capacityAmbassador, t, err)
	}
	if !ok {
		return s.deny(c, capacityPlayer, t, "not_ambassador"), nil
	}
	if c.UserID == t.UserID {
		s.log(ambassadorEvent{Event: "denied", Caller: c, Capacity: capacityAmbassador, Target: t, Outcome: outcomeSelfTarget, Reason: "self_target"})
		return ambassadorResult{Outcome: outcomeSelfTarget}, nil
	}
	return s.graduate(ctx, c, capacityAmbassador, "ready", graduatedViaAmbassador, tierReasonAmbassador, t)
}

// SelfPromote graduates the caller. Their choice, their call: any linked player,
// no confirmation.
func (s *ambassadorService) SelfPromote(ctx context.Context, c ambassadorCaller) (ambassadorResult, error) {
	self := ambassadorTarget{UserID: c.UserID, DiscordID: c.DiscordID}
	return s.graduate(ctx, c, capacitySelf, "self_promote", graduatedViaSelf, tierReasonSelf, self)
}

// Demote clears the graduated mark and tells the player. Staff only, no
// self-target. There is no player command to go down: that is by ticket.
func (s *ambassadorService) Demote(ctx context.Context, c ambassadorCaller, t ambassadorTarget) (ambassadorResult, error) {
	if !c.isStaff() {
		return s.deny(c, capacityPlayer, t, "not_staff"), nil
	}
	if c.UserID == t.UserID {
		s.log(ambassadorEvent{Event: "denied", Caller: c, Capacity: c.Staff, Target: t, Outcome: outcomeSelfTarget, Reason: "self_target"})
		return ambassadorResult{Outcome: outcomeSelfTarget}, nil
	}
	before, after, changed, err := s.update(ctx, t.UserID, func(a *AmbassadorAccount) bool {
		if !a.Graduated {
			return false
		}
		a.Graduated = false
		a.GraduatedBy, a.GraduatedVia, a.GraduatedAt = "", "", time.Time{}
		a.DemotedBy = c.UserID
		a.DemotedAt = s.now()
		a.DemotedGuildID = c.GuildID
		return true
	})
	if err != nil {
		return s.fail(c, c.Staff, t, err)
	}
	if !changed {
		s.log(ambassadorEvent{Event: "noop", Caller: c, Capacity: c.Staff, Target: t, Outcome: outcomeNoop, Reason: "not_graduated", Before: before.logView(), After: after.logView()})
		return ambassadorResult{Outcome: outcomeNoop}, nil
	}
	s.log(ambassadorEvent{Event: "demote", Caller: c, Capacity: c.Staff, Target: t, Outcome: outcomeOK, Before: before.logView(), After: after.logView()})
	res := ambassadorResult{Outcome: outcomeOK}
	res.DMFailed = s.deliverDM(ctx, c, c.Staff, t, tierDM(TierTo, TierFrom, tierReasonStaff))
	return res, nil
}
