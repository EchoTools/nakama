package server

type MatchPlayerUpdate struct {
	SessionID     string `json:"session_id"`
	IsMatchmaking *bool  `json:"is_matchmaking"`
	RoleAlignment *int   `json:"role"`
}
