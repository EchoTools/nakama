package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

// client -> nakama: request the user profile for their logged-in account.
type LoggedInUserProfileRequest struct {
	Session            uuid.UUID
	EvrID              EvrId
	ProfileRequestData ProfileRequestData
}

func (m LoggedInUserProfileRequest) Token() string {
	return "SNSLoggedInUserProfileRequest"
}

func (m LoggedInUserProfileRequest) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (r LoggedInUserProfileRequest) String() string {
	return fmt.Sprintf("LoggedInUserProfileRequest(session=%v, user_id=%v, profile_request=%v)", r.Session, r.EvrID, r.ProfileRequestData)
}

func (m *LoggedInUserProfileRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.Session) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrID.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrID.AccountId) },
		func() error { return s.StreamJson(&m.ProfileRequestData, true, NoCompression) },
	})
}

func NewLoggedInUserProfileRequest(session uuid.UUID, evrId EvrId, profileRequestData ProfileRequestData) LoggedInUserProfileRequest {
	return LoggedInUserProfileRequest{
		Session:            session,
		EvrID:              evrId,
		ProfileRequestData: profileRequestData,
	}
}
func (m *LoggedInUserProfileRequest) GetLoginSessionID() uuid.UUID {
	return m.Session
}

func (m *LoggedInUserProfileRequest) GetEvrID() EvrId {
	return m.EvrID
}

type ProfileRequestData struct {
	Defaultclientprofileid string       `json:"defaultclientprofileid"`
	Defaultserverprofileid string       `json:"defaultserverprofileid"`
	Unlocksetids           Unlocksetids `json:"unlocksetids"`
	Statgroupids           Statgroupids `json:"statgroupids"`
}

type Statgroupids struct {
	Arena           map[string]interface{} `json:"arena"`
	ArenaPracticeAI map[string]interface{} `json:"arena_practice_ai"`
	ArenaPublicAI   map[string]interface{} `json:"arena_public_ai"`
	Combat          map[string]interface{} `json:"combat"`
}

type Unlocksetids struct {
	All map[string]interface{} `json:"all"`
}
