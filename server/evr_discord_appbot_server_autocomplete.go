package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// MaxRegionPingMs is the maximum ping (in ms) a non-privileged player
	// can select in the region autocomplete. Regions above this threshold
	// (or with no ping data) are hidden unless the user is an enforcer or
	// global operator.
	MaxRegionPingMs = 90
)

type RegionAutocompleteData struct {
	RegionCode string
	Location   string
	Total      int
	Available  int
	MinPing    int
	MaxPing    int
}

func (c RegionAutocompleteData) Description() string {
	if c.MinPing == c.MaxPing {
		return fmt.Sprintf("%s -- %dms [%d/%d]", c.Location, c.MinPing, c.Total-c.Available, c.Total)
	}

	return fmt.Sprintf("%s -- %dms-%dms [%d/%d]", c.Location, c.MinPing, c.MaxPing, c.Total-c.Available, c.Total)
}

func (d *DiscordAppBot) autocompleteRegions(ctx context.Context, logger runtime.Logger, userID string, groupID string, mode evr.Symbol) ([]*discordgo.ApplicationCommandOptionChoice, error) {

	latencyHistory := NewLatencyHistory()
	if err := StorableRead(ctx, d.nk, userID, latencyHistory, false); err != nil && status.Code(err) != codes.NotFound {
		logger.Error("Failed to read latency history", zap.Error(err))
		return nil, err
	}

	rtts := latencyHistory.AverageRTTs(true)

	// Get the available servers
	minSize := 0
	maxSize := 100
	query := fmt.Sprintf("+label.broadcaster.group_ids:/(%s)/ +label.broadcaster.region_codes:default", Query.QuoteStringValue(groupID))

	matches, err := d.nk.MatchList(ctx, 100, true, "", &minSize, &maxSize, query)
	if err != nil {
		logger.Error("Failed to list servers", zap.Error(err))
		return nil, err
	}

	regionDatas := make(map[string]*RegionAutocompleteData, len(matches))
	for _, m := range matches {
		label := MatchLabel{}
		if err := json.Unmarshal([]byte(m.GetLabel().GetValue()), &label); err != nil {
			logger.Error("Failed to unmarshal match label", zap.Error(err))
			continue
		}
		if label.GameServer == nil {
			continue
		}
		regionCode := label.GameServer.LocationRegionCode(true, true)
		extIP := label.GameServer.Endpoint.ExternalIP.String()
		c, found := regionDatas[regionCode]
		if !found {
			c = &RegionAutocompleteData{
				RegionCode: regionCode,
				Location:   label.GameServer.Location(true),
				Total:      1,
				Available:  0,
				MinPing:    rtts[extIP],
				MaxPing:    rtts[extIP],
			}
			regionDatas[regionCode] = c
		} else {
			c.Total++
		}

		if label.Mode == evr.ModeUnloaded {
			c.Available++

			if rtts[extIP] < c.MinPing {
				c.MinPing = rtts[extIP]
			}
			if rtts[extIP] > c.MaxPing {
				c.MaxPing = rtts[extIP]
			}
		}

	}

	// Only restrict regions by ping for public arena mode
	restrictPing := mode == evr.ModeArenaPublic
	if restrictPing {
		privileged, err := isRegionPrivileged(ctx, d.db, d.nk, userID, groupID)
		if err != nil {
			logger.Warn("Failed to check region privileges, defaulting to restricted", zap.Error(err))
		}
		if privileged {
			restrictPing = false
		}
	}

	choices := filterAndSortRegionChoices(regionDatas, !restrictPing)
	return choices, nil
}

// isRegionPrivileged returns true if the user is an enforcer in the guild or a global operator,
// granting them access to all regions regardless of ping.
func isRegionPrivileged(ctx context.Context, db *sql.DB, nk runtime.NakamaModule, userID string, groupID string) (bool, error) {
	// Check global operator first
	isGlobalOp, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators)
	if err != nil {
		return false, fmt.Errorf("failed to check global operator: %w", err)
	}
	if isGlobalOp {
		return true, nil
	}

	// Check guild enforcer
	gg, err := GuildGroupLoad(ctx, nk, groupID)
	if err != nil {
		return false, fmt.Errorf("failed to load guild group: %w", err)
	}
	return gg.IsEnforcer(userID), nil
}

// filterAndSortRegionChoices filters out unavailable regions and sorts by latency.
// When privileged is false, regions with MinPing > MaxRegionPingMs or MinPing == 0
// (no ping data) are excluded.
// Exported for testing.
func filterAndSortRegionChoices(regionDatas map[string]*RegionAutocompleteData, privileged bool) []*discordgo.ApplicationCommandOptionChoice {
	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(regionDatas))

	datas := make([]*RegionAutocompleteData, 0, len(regionDatas))

	// Filter out regions with no available servers,
	// and for non-privileged users, regions with high or unknown ping.
	for _, data := range regionDatas {
		if data.Available == 0 {
			continue
		}
		if !privileged && (data.MinPing == 0 || data.MinPing > MaxRegionPingMs) {
			continue
		}
		datas = append(datas, data)
	}

	// Sort by latency (min ping first, then max ping as tiebreaker).
	// Regions with no ping data (MinPing == 0) sort to the end.
	slices.SortFunc(datas, func(a, b *RegionAutocompleteData) int {
		aHasPing := a.MinPing > 0
		bHasPing := b.MinPing > 0

		if aHasPing != bHasPing {
			if aHasPing {
				return -1
			}
			return 1
		}

		if a.MinPing < b.MinPing {
			return -1
		}
		if a.MinPing > b.MinPing {
			return 1
		}

		// Tiebreaker: sort by max ping
		if a.MaxPing < b.MaxPing {
			return -1
		}
		if a.MaxPing > b.MaxPing {
			return 1
		}

		return 0
	})

	for _, data := range datas {
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  data.Description(),
			Value: data.RegionCode,
		})
	}

	return choices
}
