package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Journal suspensions and the invite RPCs (#765).
//
// invite/create and invite/regions used to check only the guild-role suspension
// (GuildGroup.IsSuspended, through inviteGuildAccess). The enforcement journal's
// suspensions, which the lobby seat enforces (enforceJoinSuspension), were not
// asked, so a player refused a public game could still create a private one and
// hand out codes. This file asks the same journals, with the same alt rules as
// the seat, and answers the existing "denied" token.

// inviteSuspensions is the journal suspensions that apply to the caller in a
// guild, per mode, after the alt rules.
//
// Fail-closed: an error here refuses the call. It is a seam so tests can supply
// journals without a Discord bot, login history or database.
var inviteSuspensions = defaultInviteSuspensions

// inviteSuspensionSet maps each mode to the suspension that applies to it.
type inviteSuspensionSet map[evr.Symbol]GuildEnforcementRecord

// invitePublicCounterpart is the public mode of a private invite mode.
var invitePublicCounterpart = map[evr.Symbol]evr.Symbol{
	evr.ModeArenaPrivate:  evr.ModeArenaPublic,
	evr.ModeCombatPrivate: evr.ModeCombatPublic,
	evr.ModeSocialPrivate: evr.ModeSocialPublic,
}

// inviteModes are the modes an invite game can be: every one is private.
var inviteModes = []evr.Symbol{evr.ModeArenaPrivate, evr.ModeCombatPrivate, evr.ModeSocialPrivate}

// refuses reports whether the caller may not create a game in mode.
//
// A record that covers the mode refuses it. A private mode is also refused when
// the record covers its public counterpart, unless the record allows private
// lobbies: that flag is what lets a suspended player into private games.
func (s inviteSuspensionSet) refuses(mode evr.Symbol) bool {
	if _, ok := s[mode]; ok {
		return true
	}
	if pub, ok := invitePublicCounterpart[mode]; ok {
		if r, ok := s[pub]; ok && !r.SuspensionExcludesPrivateLobbies() {
			return true
		}
	}
	return false
}

// refusesEvery reports whether every invite game is refused, which is what a
// request that names no experience can be told.
func (s inviteSuspensionSet) refusesEvery() bool {
	for _, m := range inviteModes {
		if !s.refuses(m) {
			return false
		}
	}
	return true
}

// inviteSuspensionsFrom applies the seat's rules to loaded journals: the merged
// record per (guild, mode) covers the player and the alts, and when alt
// suspensions do not count the player's own record is used instead, since the
// merge keeps whichever expires last and an alt's can hide the player's.
func inviteSuspensionsFrom(userID, groupID string, journals GuildEnforcementJournalList, inheritance map[string][]string, altsCount bool) (inviteSuspensionSet, error) {
	merged, err := CheckEnforcementSuspensions(journals, inheritance)
	if err != nil {
		return nil, fmt.Errorf("check suspensions: %w", err)
	}
	var own ActiveGuildEnforcements
	set := inviteSuspensionSet{}
	for mode, r := range merged[groupID] {
		if r.IsExpired() {
			continue
		}
		if r.UserID != userID && !altsCount {
			if own == nil {
				if own, err = CheckOwnEnforcementSuspensions(userID, journals, inheritance); err != nil {
					return nil, fmt.Errorf("check own suspensions: %w", err)
				}
			}
			o, ok := own[groupID][mode]
			if !ok || o.IsExpired() {
				continue
			}
			r = o
		}
		set[mode] = r
	}
	return set, nil
}

// defaultInviteSuspensions loads what the seat loads: the journals of the player
// and the alts login found, and the guild's alt setting. Without the bot there is
// no registry to ask, and the absence of a suspension is not assumed.
func defaultInviteSuspensions(ctx context.Context, nk runtime.NakamaModule, userID string, groupID uuid.UUID) (inviteSuspensionSet, error) {
	appBot := globalAppBot.Load()
	if appBot == nil || appBot.guildGroupRegistry == nil {
		return nil, errors.New("the Discord bot is not running")
	}
	history := NewLoginHistory(userID)
	if err := StorableRead(ctx, nk, userID, history, false); err != nil && status.Code(err) != codes.NotFound {
		return nil, fmt.Errorf("login history: %w", err)
	}
	altIDs, _ := history.AlternateIDs()
	if detector := GetCGNATDetector(); detector != nil {
		altIDs = filterStrongAlts(history, altIDs, detector)
	}
	journals, err := EnforcementJournalsLoad(ctx, nk, append(altIDs, userID))
	if err != nil {
		return nil, fmt.Errorf("journals: %w", err)
	}
	gg := appBot.guildGroupRegistry.Get(groupID.String())
	altsCount := !history.IgnoreSuspensionsOfAltAccounts && (gg == nil || gg.RejectPlayersWithSuspendedAlternates)
	return inviteSuspensionsFrom(userID, groupID.String(), journals, appBot.guildGroupRegistry.InheritanceByParentGroupID(), altsCount)
}

// inviteCheckSuspension answers "denied" when a journal suspension covers the
// requested mode, or every invite mode when none was requested (mode 0).
func inviteCheckSuspension(ctx context.Context, nk runtime.NakamaModule, rpc, userID string, groupID uuid.UUID, mode evr.Symbol) error {
	set, err := inviteSuspensions(ctx, nk, userID, groupID)
	if err != nil {
		return fmt.Errorf("invite %s: suspensions: %w", rpc, err)
	}
	if (mode != 0 && set.refuses(mode)) || (mode == 0 && set.refusesEvery()) {
		return inviteRefusal("denied")
	}
	return nil
}
