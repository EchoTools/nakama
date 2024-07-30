package server

import (
	"time"

	"github.com/gofrs/uuid/v5"
)

var (
	GameModes = map[string][]string{
		"arena":  {"arena"},
		"social": {"social"},
		"combat": {"fission", "combustion", "dyson", "gauss"},
	}

	roleLimits = map[string]map[int]int{
		"social": {
			SocialRole:    12,
			ModeratorRole: 2,
		},
		"arena": {
			BlueRole:      4,
			OrangeRole:    4,
			SpectatorRole: 5,
		},
		"combat": {
			BlueRole:      5,
			OrangeRole:    5,
			SpectatorRole: 5,
		},
	}
)

const (
	UnassignedRole int = -1 + iota
	BlueRole
	OrangeRole
	SpectatorRole
	SocialRole
	ModeratorRole
)

type MatchMeta struct {
	Visibility  string
	CreatedAt   time.Time
	CreatedBy   uuid.UUID
	GroupID     uuid.UUID
	GuildID     string
	GuildName   string
	Mode        string
	Level       string
	Features    []string
	ServerIntIP string
	ServerExtIP string
	ServerPort  string
	Tags        []string
}

func NewMatchMeta(visibility string, createdBy uuid.UUID, groupID uuid.UUID, guildID, guildName, mode, level string, features []string, serverIntIP, serverExtIP, serverPort string, tags []string) *MatchMeta {
	return &MatchMeta{
		Visibility:  visibility,
		CreatedAt:   time.Now().UTC(),
		CreatedBy:   createdBy,
		GroupID:     groupID,
		GuildID:     guildID,
		GuildName:   guildName,
		Mode:        mode,
		Level:       level,
		Features:    features,
		ServerIntIP: serverIntIP,
		ServerExtIP: serverExtIP,
		ServerPort:  serverPort,
		Tags:        tags,
	}
}

func (m MatchMeta) IsPublic() bool {
	return m.Visibility == "public"
}

func (MatchMeta) SizeLimit() int {
	return MatchMaxSize
}

func (m MatchMeta) PlayerLimit() int {
	if !m.IsPublic() {
		return m.SizeLimit()
	}
	count := 0
	for _, r := range []int{BlueRole, OrangeRole, SocialRole} {
		count += roleLimits[m.Mode][r]
	}
	return count
}

func (m *MatchMeta) SpectatorLimit() int {
	if !m.IsPublic() {
		return m.SizeLimit()
	}
	return m.SizeLimit() - m.PlayerLimit()
}

func (m *MatchMeta) RoleLimit(r int) int {
	if roleLimits[m.Mode][r] == 0 { // Ensure the role is valid.
		return 0
	}
	if !m.IsPublic() {
		return m.SizeLimit()
	}
	return roleLimits[m.Mode][r]
}

func (m *MatchMeta) DefaultRole() int {
	if !m.IsPublic() {
		return UnassignedRole
	}
	switch m.Mode {
	case "social":
		return SocialRole
	case "arena", "combat":
		return BlueRole
	default:
		return UnassignedRole
	}
}

type MatchState struct {
	meta           *MatchMeta
	open           bool
	startedAt      time.Time
	presenceMap    map[string]*EvrMatchPresence
	emptyTicks     int64                // The number of ticks the match has been empty.
	joinTimestamps map[string]time.Time // The timestamps of when players joined the match. map[sessionId]time.Time
	broadcaster    *EvrMatchPresence    // The broadcaster's presence
	alignments     map[string]int       // map[userID]TeamIndex
}

func NewMatchState(meta *MatchMeta) *MatchState {
	return &MatchState{
		meta:           meta,
		presenceMap:    make(map[string]*EvrMatchPresence),
		joinTimestamps: make(map[string]time.Time),
		alignments:     make(map[string]int),
	}
}

func (s *MatchState) Size() int {
	return len(s.presenceMap)
}

func (s *MatchState) PlayerCount() int {
	count := 0
	for _, p := range s.presenceMap {
		if p.RoleAlignment != SpectatorRole && p.RoleAlignment != ModeratorRole {
			count++
		}
	}
	return count
}

func (s *MatchState) SpectatorCount() int {
	return s.Size() - s.PlayerCount()
}

func (s *MatchState) OpenPlayerSlots() int {
	if s.meta.IsPublic() {
		return min(s.meta.PlayerLimit()-s.PlayerCount(), s.OpenSlots())
	}
	return s.OpenSlots()
}

func (s *MatchState) IsPriorityForMode() bool {
	tag := "priority_mode_" + s.meta.Mode
	for _, t := range s.meta.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

func (s *MatchState) OpenSpectatorSlots() int {
	if s.meta.IsPublic() {
		return min(s.meta.SizeLimit()-s.meta.PlayerLimit()+s.SpectatorCount(), s.OpenSlots())
	}
	return s.OpenSlots()
}

func (s *MatchState) OpenSlots() int {
	return s.meta.SizeLimit() - s.Size()
}

func (s *MatchState) RoleCount(r int) int {
	if !s.meta.IsPublic() {
		return len(s.presenceMap)
	}

	count := 0
	for _, p := range s.presenceMap {
		if p.RoleAlignment == r {
			count++
		}
	}
	return count
}

func (s *MatchState) OpenSlotsByRole(r int) int {
	if s.meta.RoleLimit(r) == 0 {
		return 0
	} else if !s.meta.IsPublic() {
		return s.OpenSlots()
	} else if r == UnassignedRole && s.OpenPlayerSlots() == 0 {
		return 0
	} else if (r == SpectatorRole || r == ModeratorRole) && s.OpenSpectatorSlots() == 0 {
		return 0
	}
	return min(s.meta.RoleLimit(r)-s.RoleCount(r), s.OpenPlayerSlots())
}

func (s *MatchState) SelectRole(desired int) (int, bool) {
	if !s.meta.IsPublic() {
		return desired, s.OpenSlotsByRole(desired) > 0
	}

	if desired == UnassignedRole {
		desired = s.meta.DefaultRole()
	}

	if desired == ModeratorRole || desired == SpectatorRole {
		return desired, s.OpenSlotsByRole(desired) > 0
	}

	if desired == SocialRole {
		return desired, s.OpenSlotsByRole(desired) > 0
	}

	if s.RoleCount(BlueRole) == s.RoleCount(OrangeRole) {
		return desired, s.OpenSlotsByRole(desired) > 0
	}

	if s.RoleCount(BlueRole) < s.RoleCount(OrangeRole) {
		return BlueRole, s.OpenSlotsByRole(BlueRole) > 0
	}

	return OrangeRole, s.OpenSlotsByRole(OrangeRole) > 0
}
