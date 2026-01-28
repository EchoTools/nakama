package evr

import (
	"encoding/binary"
	"fmt"
)

// BotDifficulty levels matching nevr-server's BotDifficulty enum
type BotDifficulty uint32

const (
	BotDifficultyBeginner     BotDifficulty = 0
	BotDifficultyIntermediate BotDifficulty = 1
	BotDifficultyAdvanced     BotDifficulty = 2
	BotDifficultyExpert       BotDifficulty = 3
	BotDifficultyMaster       BotDifficulty = 4
)

// Bot team constants
const (
	BotTeamBlue   uint32 = 0
	BotTeamOrange uint32 = 1
)

// SNSLobbySetSpawnBotOnServer instructs the game server to spawn bots.
// Symbol: 0xa0687d9799640878
// Size: 48 bytes
type SNSLobbySetSpawnBotOnServer struct {
	MessageType       uint32   // 4 bytes - message identifier (unused)
	MatchID           uint32   // 4 bytes - match identifier
	TeamID            uint32   // 4 bytes - 0=blue, 1=orange
	BotCount          uint32   // 4 bytes - 1-3 bots
	Difficulty        uint32   // 4 bytes - 0-4 (BEGINNER to MASTER)
	LoadoutID         uint32   // 4 bytes - loadout preset
	LoadoutData       [12]byte // 12 bytes - additional loadout info
	SpawnPositionSeed uint32   // 4 bytes - RNG seed for spawn positions
	Reserved          uint32   // 4 bytes - reserved
	Reserved2         uint32   // 4 bytes - padding to 48 bytes
}

func (m *SNSLobbySetSpawnBotOnServer) String() string {
	return fmt.Sprintf("SNSLobbySetSpawnBotOnServer{MatchID=%d, TeamID=%d, BotCount=%d, Difficulty=%d}",
		m.MatchID, m.TeamID, m.BotCount, m.Difficulty)
}

func (m *SNSLobbySetSpawnBotOnServer) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.MessageType) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.MatchID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TeamID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.BotCount) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Difficulty) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LoadoutID) },
		func() error {
			loadoutSlice := m.LoadoutData[:]
			return s.StreamBytes(&loadoutSlice, 12)
		},
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SpawnPositionSeed) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Reserved) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Reserved2) },
	})
}

// NewSNSLobbySetSpawnBotOnServer creates a bot spawn message
func NewSNSLobbySetSpawnBotOnServer(matchID uint32, teamID uint32, botCount uint32, difficulty BotDifficulty) *SNSLobbySetSpawnBotOnServer {
	return &SNSLobbySetSpawnBotOnServer{
		MessageType:       0,
		MatchID:           matchID,
		TeamID:            teamID,
		BotCount:          botCount,
		Difficulty:        uint32(difficulty),
		LoadoutID:         0,
		LoadoutData:       [12]byte{},
		SpawnPositionSeed: 0,
		Reserved:          0,
		Reserved2:         0,
	}
}
