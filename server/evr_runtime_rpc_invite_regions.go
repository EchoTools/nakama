package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// invite/regions answers the wizard's SERVER REGION row with what /create's
// region autocomplete computes for the caller: the live game servers' own region
// codes, each with the caller's average RTT, from their stored latency history.
// There are no generic regions (no "us-west"); the server is chosen by the
// player's ping, and "default" is that choice made for them. Grouping servers
// into clusters by measured latency is a later design.

// ErrNoLatencyHistory: the account has no stored latency history, so no region
// can be offered and no server can be chosen for it. invite/regions and
// invite/create answer it with the stable token "no_history".
var ErrNoLatencyHistory = errors.New("no latency history exists for your account; play some matches first to establish server latency data")

// inviteRegionSet is what the region seams answer: the regions /create would
// offer, in its order, and every region code the caller's guild has a server in.
// A code that is Known but not Listed has no free server right now.
type inviteRegionSet struct {
	Listed []*RegionAutocompleteData
	Known  map[string]bool
}

var (
	// inviteHasHistory reports whether the account has stored latency history.
	inviteHasHistory = defaultInviteHasHistory
	// inviteRegions lists the regions the account can choose from.
	inviteRegions = defaultInviteRegions
	// inviteGuildAccess reports whether the caller is a member of the guild group
	// and not suspended there.
	inviteGuildAccess = defaultInviteGuildAccess
)

// defaultInviteGuildAccess is handleCreateMatch's membership and suspension
// check, asked before anything about the guild's servers is revealed: the active
// group is a stored choice, and the player may have left or been suspended in it
// since. Without the bot there is no registry to ask, and membership is not assumed.
func defaultInviteGuildAccess(ctx context.Context, nk runtime.NakamaModule, userID string, groupID uuid.UUID) (bool, error) {
	appBot := globalAppBot.Load()
	if appBot == nil || appBot.guildGroupRegistry == nil {
		return false, errors.New("the Discord bot is not running")
	}
	groups, err := GuildUserGroupsList(ctx, nk, appBot.guildGroupRegistry, userID)
	if err != nil {
		return false, err
	}
	group, ok := groups[groupID.String()]
	if !ok {
		return false, nil
	}
	return !group.IsSuspended(userID, nil), nil
}

func defaultInviteHasHistory(ctx context.Context, nk runtime.NakamaModule, userID string) (bool, error) {
	history := NewLatencyHistory()
	if err := StorableRead(ctx, nk, userID, history, false); err != nil && status.Code(err) != codes.NotFound {
		return false, err
	}
	return len(history.AverageRTTs(true)) > 0, nil
}

// defaultInviteRegions is /create's list for a private game: no ping cap (the cap
// is public arena's), so a measured region beyond MaxRegionPingMs is listed and
// flagged, and a region the guild has no server in is not invented. A server
// with no location has no region code and is reachable only through "default".
func defaultInviteRegions(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, groupID uuid.UUID) (inviteRegionSet, error) {
	view, err := regionsFor(ctx, logger, nk, userID, groupID.String())
	if err != nil {
		return inviteRegionSet{}, err
	}
	delete(view.All, "")
	set := inviteRegionSet{Known: make(map[string]bool, len(view.All))}
	for code := range view.All {
		set.Known[code] = true
	}
	set.Listed = filterAndSortRegions(view.All, true)
	return set, nil
}

type inviteRegionsRequest struct {
	Experience string `json:"experience"`
	Mode       string `json:"mode"`
}

type inviteRegionRow struct {
	Code      string `json:"code"`
	Location  string `json:"location"`
	PingMin   int    `json:"ping_min_ms"`
	PingMax   int    `json:"ping_max_ms"`
	Available int    `json:"available"`
	Total     int    `json:"total"`
	InRange   bool   `json:"in_range"`
}

// inRangePing: the caller's ping to the region is measured and within the range
// /create allows for public arena.
func inRangePing(ping int) bool { return ping > 0 && ping <= MaxRegionPingMs }

// InviteRegionsRPC lists the regions the caller can ask invite/create for, the
// nearest first, after a "default" row (the best server by ping). It reads and
// writes nothing, and is not rate-limited: the wizard calls it when the row opens.
func InviteRegionsRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	rec := inviteRecordFrom(ctx)
	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if userID == "" {
		return "", runtime.NewError("unauthenticated", StatusUnauthenticated)
	}

	req := inviteRegionsRequest{}
	if strings.TrimSpace(payload) != "" {
		if err := json.Unmarshal([]byte(payload), &req); err != nil {
			return "", inviteRefusal("invalid")
		}
	}
	rec.args["experience"], rec.args["mode"] = req.Experience, req.Mode
	// Every invite game is private, so the list does not vary with the mode; an
	// experience is still checked against its mode as invite/create does.
	if req.Experience != "" {
		if _, ok := parseInviteMode(req.Experience, req.Mode); !ok {
			return "", inviteRefusal("invalid")
		}
	}

	groupID, err := inviteActiveGroup(ctx, nk, userID)
	if err != nil {
		return "", fmt.Errorf("invite regions: active guild: %w", err)
	}
	if groupID == uuid.Nil {
		return "", inviteRefusal("denied")
	}
	rec.groupID = groupID.String()
	if member, err := inviteGuildAccess(ctx, nk, userID, groupID); err != nil {
		return "", fmt.Errorf("invite regions: guild access: %w", err)
	} else if !member {
		return "", inviteRefusal("denied")
	}

	hasHistory, err := inviteHasHistory(ctx, nk, userID)
	if err != nil {
		return "", fmt.Errorf("invite regions: latency history: %w", err)
	}
	if !hasHistory {
		return "", inviteRefusal("no_history")
	}

	set, err := inviteRegions(ctx, logger, nk, userID, groupID)
	if err != nil {
		return "", fmt.Errorf("invite regions: %w", err)
	}

	rows := make([]inviteRegionRow, 0, len(set.Listed)+1)
	best := inviteRegionRow{Code: RegionDefault}
	for _, r := range set.Listed {
		if r.MinPing > 0 && (best.PingMin == 0 || r.MinPing < best.PingMin) {
			best.PingMin = r.MinPing
		}
		best.Available += r.Available
		best.Total += r.Total
	}
	best.PingMax = best.PingMin
	best.InRange = inRangePing(best.PingMin)
	rows = append(rows, best)
	for _, r := range set.Listed {
		rows = append(rows, inviteRegionRow{
			Code: r.RegionCode, Location: r.Location,
			PingMin: r.MinPing, PingMax: r.MaxPing,
			Available: r.Available, Total: r.Total,
			InRange: inRangePing(r.MinPing),
		})
	}
	out, err := json.Marshal(rows)
	if err != nil {
		return "", fmt.Errorf("invite regions: %w", err)
	}
	return string(out), nil
}
