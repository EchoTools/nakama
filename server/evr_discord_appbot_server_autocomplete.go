package server

import (
	"context"
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

func (d *DiscordAppBot) autocompleteRegions(ctx context.Context, logger runtime.Logger, userID string, groupID string) ([]*discordgo.ApplicationCommandOptionChoice, error) {

	latencyHistory := NewLatencyHistory()
	if err := StorageRead(ctx, d.nk, userID, latencyHistory, false); err != nil && status.Code(err) != codes.NotFound {
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

	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(regionDatas))

	datas := make([]*RegionAutocompleteData, 0, len(regionDatas))

	for _, data := range regionDatas {
		datas = append(datas, data)
	}

	// Sort the data by min ping
	slices.SortFunc(datas, func(a, b *RegionAutocompleteData) int {
		// Sort by if there are any available servers
		if a.Available == 0 && b.Available > 0 {
			return 1
		}
		if a.Available > 0 && b.Available == 0 {
			return -1
		}

		// Sort by min ping
		if a.MinPing < b.MinPing {
			return -1
		}
		if a.MinPing > b.MinPing {
			return 1
		}

		// Sort by max ping
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

	return choices, nil
}
