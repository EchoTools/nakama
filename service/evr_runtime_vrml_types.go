package service

var (
	StorageKeyVRMLPlayerMap = "PlayerMap"
)

type VRMLPlayerListItems struct {
	Season    VRMLPlayerListSeason `json:"season"`
	Players   []VRMLPlayerListItem `json:"players"`
	Total     int64                `json:"total"`
	Pos       int64                `json:"pos"`
	PosMin    int64                `json:"posMin"`
	NbPerPage int64                `json:"nbPerPage"`
}

type VRMLPlayerListSeason struct {
	GameName     string `json:"gameName"`
	GameURLShort string `json:"gameUrlShort"`
	GameActive   bool   `json:"gameActive"`
	SeasonID     string `json:"seasonID"`
	SeasonName   string `json:"seasonName"`
	IsCurrent    bool   `json:"isCurrent"`
}

type VRMLPlayerListItem struct {
	Pos                    int64  `json:"pos"`
	PlayerID               string `json:"playerID"`
	UserID                 string `json:"userID"`
	PlayerName             string `json:"playerName"`
	UserLogo               string `json:"userLogo"`
	Country                string `json:"country"`
	Nationality            string `json:"nationality"`
	RoleID                 string `json:"roleID"`
	Role                   string `json:"role"`
	IsTeamOwner            bool   `json:"isTeamOwner"`
	IsTeamStarter          bool   `json:"isTeamStarter"`
	TeamID                 string `json:"teamID"`
	TeamName               string `json:"teamName"`
	TeamNameFull           string `json:"teamNameFull"`
	TeamLogo               string `json:"teamLogo"`
	HonoursMention         any    `json:"honoursMention"`
	HonoursMentionLogo     any    `json:"honoursMentionLogo"`
	CooldownID             any    `json:"cooldownID"`
	CooldownNote           any    `json:"cooldownNote"`
	CooldownDateExpiresUTC any    `json:"cooldownDateExpiresUTC"`
}
