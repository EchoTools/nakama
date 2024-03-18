package evr

import (
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/samber/lo"
)

type ChannelInfoResponse struct {
	ChannelInfo ChannelInfoResource
}

func (m *ChannelInfoResponse) Token() string {
	return "SNSChannelInfoResponse"
}

func (m *ChannelInfoResponse) Symbol() Symbol {
	return ToSymbol(m.Token())
}

type ChannelInfoResource struct {
	Groups []ChannelGroup `json:"group" validate:"required,notempty"`
}

func (m *ChannelInfoResponse) Stream(s *EasyStream) error {
	return s.StreamJson(&m.ChannelInfo, false, ZlibCompression)
}

func (m *ChannelInfoResponse) String() string {
	channelNames := lo.Map(m.ChannelInfo.Groups, func(group ChannelGroup, i int) string {
		return group.Name
	})
	return fmt.Sprintf("%s(%s)", m.Token(), strings.Join(channelNames, "; "))
}

func NewSNSChannelInfoResponse(channelInfo *ChannelInfoResource) *ChannelInfoResponse {
	return &ChannelInfoResponse{
		ChannelInfo: *channelInfo,
	}
}

type ChannelGroup struct {
	ChannelUuid  string `json:"channeluuid"`
	Name         string `json:"name" validate:"required,notblank,ascii"`
	Description  string `json:"description" validate:"required,notblank,ascii"`
	Rules        string `json:"rules" validate:"required,notblank,ascii"`
	RulesVersion uint64 `json:"rules_version" validate:"required,notblank,gte=0"`
	Link         string `json:"link" validate:"required,notblank,http_url"`
	Priority     uint64 `json:"priority" validate:"required,notblank,gte=0,unique"`
	RAD          bool   `json:"_rad" validate:"required,notblank"`
}

func NewChannelGroup() ChannelGroup {
	return ChannelGroup{
		ChannelUuid:  strings.ToUpper(uuid.Must(uuid.NewV4()).String()),
		Name:         "PLAYGROUND",
		Description:  "Classic Echo VR social lobbies.",
		Rules:        "1. Only use this channel for testing.\n2. Act responsibly.\n3. Act legally.",
		RulesVersion: 1,
		Link:         "https://en.wikipedia.org/wiki/Lone_Echo",
		Priority:     0,
		RAD:          true,
	}
}

func NewChannelInfoResource() ChannelInfoResource {
	return ChannelInfoResource{
		Groups: []ChannelGroup{
			{
				ChannelUuid:  "90DD4DB5-B5DD-4655-839E-FDBE5F4BC0BF",
				Name:         "LOBBY A",
				Description:  "PLACEHOLDER LOBBY",
				Rules:        "",
				RulesVersion: 1,
				Link:         "https://en.wikipedia.org/wiki/Lone_Echo",
				Priority:     0,
				RAD:          true,
			},
			{
				ChannelUuid:  "DD9C48DF-C495-4EF3-B317-4FD6364F329D",
				Name:         "LOBBY B",
				Description:  "PLACEHOLDER LOBBY",
				Rules:        "",
				RulesVersion: 1,
				Link:         "https://en.wikipedia.org/wiki/Lone_Echo",
				Priority:     1,
				RAD:          true,
			},
			{
				ChannelUuid:  "937CE604-5DC7-431F-812B-C7C25B4B37B6",
				Name:         "LOBBY C",
				Description:  "PLACEHOLDER LOBBY",
				Rules:        "",
				RulesVersion: 1,
				Link:         "https://en.wikipedia.org/wiki/Lone_Echo",
				Priority:     2,
				RAD:          true,
			},
			{
				ChannelUuid:  "EF663D3F-D947-484A-BA7E-8C5ED7FED1A6",
				Name:         "LOBBY D",
				Description:  "PLACEHOLDER LOBBY",
				Rules:        "",
				RulesVersion: 1,
				Link:         "https://en.wikipedia.org/wiki/Lone_Echo",
				Priority:     3,
				RAD:          true,
			},
		},
	}
}
