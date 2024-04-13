package evr

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/gofrs/uuid/v5"
)

// PlatformCode represents the platforms on which a client may be operating.
type PlatformCode uint64

const (
	XPlatformIdSize = 16 // 16 bytes

	STM     PlatformCode = iota // Steam
	PSN                         // Playstation
	XBX                         // Xbox
	OVR_ORG                     // Oculus VR user
	OVR                         // Oculus VR
	BOT                         // Bot/AI
	DMO                         // Demo (no ovr)
	TEN                         // Tencent
)

// EvrId represents an identifier for a user on the platform.
type EvrId struct {
	PlatformCode PlatformCode
	AccountId    uint64
}

func (e EvrId) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.Token())
}

func (e *EvrId) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		*e = EvrId{}
	}
	parsed, err := ParseEvrId(s)
	if err != nil {
		return err
	}
	*e = *parsed
	return nil
}

func (xpi *EvrId) Valid() bool {
	return xpi.PlatformCode > STM && xpi.PlatformCode < TEN && xpi.AccountId > 0
}

func (xpi *EvrId) Nil() bool {
	return xpi.PlatformCode == 0 && xpi.AccountId == 0
}

func (xpi *EvrId) NotNil() bool {
	return xpi.PlatformCode != 0 && xpi.AccountId != 0
}

func (xpi *EvrId) UUID() uuid.UUID {
	if xpi.PlatformCode == 0 || xpi.AccountId == 0 {
		return uuid.Nil
	}
	return uuid.NewV5(uuid.NamespaceOID, xpi.Token())
}

// Parse parses a string into a given platform identifier.
func ParseEvrId(s string) (*EvrId, error) {
	// Obtain the position of the last dash.
	dashIndex := strings.LastIndex(s, "-")
	if dashIndex < 0 {
		return nil, fmt.Errorf("invalid format: %s", s)
	}

	// Split it there
	platformCodeStr := s[:dashIndex]
	accountIdStr := s[dashIndex+1:]

	// Determine the platform code.
	platformCode := PlatformCode(0).Parse(platformCodeStr)

	// Try to parse the account identifier
	accountId, err := strconv.ParseUint(accountIdStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse account identifier: %v", err)
	}

	// Create the identifier
	platformId := &EvrId{PlatformCode: platformCode, AccountId: accountId}
	return platformId, nil
}

func (xpi *EvrId) String() string {
	return xpi.Token()
}

func (xpi *EvrId) Token() string {
	return fmt.Sprintf("%s-%d", xpi.PlatformCode.String(), xpi.AccountId)
}

func (xpi *EvrId) Equals(other *EvrId) bool {
	return xpi.PlatformCode == other.PlatformCode && xpi.AccountId == other.AccountId
}

func (xpi *EvrId) IsNil() bool {
	return xpi.PlatformCode == 0 && xpi.AccountId == 0
}

func (xpi *EvrId) IsNotNil() bool {
	return xpi.PlatformCode != 0 && xpi.AccountId != 0
}

func (xpi *EvrId) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &xpi.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &xpi.AccountId) },
	})
}

func NewEchoUserId(platformCode PlatformCode, accountId uint64) *EvrId {
	return &EvrId{PlatformCode: platformCode, AccountId: accountId}
}

// GetPrefix obtains a platform prefix string for a given PlatformCode.
func (code PlatformCode) GetPrefix() string {
	// Try to obtain a name for this platform code.
	name := code.String()

	// If we could obtain one, the prefix should just be the same as the name, but with underscores represented as dashes.
	if name != "" {
		return name
	}

	// An unknown/invalid platform is denoted with the value returned below.
	return "???"
}

// GetDisplayName obtains a display name for a given PlatformCode.
func (code PlatformCode) GetDisplayName() string {
	// Switch on the provided platform code and return a display name.
	switch code {
	case STM:
		return "Steam"
	case PSN:
		return "Playstation"
	case XBX:
		return "Xbox"
	case OVR_ORG:
		return "Oculus VR (ORG)"
	case OVR:
		return "Oculus VR"
	case BOT:
		return "Bot"
	case DMO:
		return "Demo"
	case TEN:
		return "Tencent" // TODO: Verify, this is only suspected to be the target of "TEN".
	default:
		return "Unknown"
	}
}

// Parse parses a string generated from PlatformCode's String() method back into a PlatformCode.
func (code PlatformCode) Parse(s string) PlatformCode {
	// Convert any underscores in the string to dashes.
	s = strings.ReplaceAll(s, "-", "_")

	// Get the enum option to represent this.
	if result, ok := platformCodeFromString(s); ok {
		return result
	}
	return 0
}

// platformCodeToString converts a PlatformCode to its string representation.
func (code PlatformCode) String() string {
	return code.Abbrevation()
}

func (code PlatformCode) Abbrevation() string {
	switch code {
	case STM:
		return "STM"
	case PSN:
		return "PSN"
	case XBX:
		return "XBX"
	case OVR_ORG:
		return "OVR_ORG"
	case OVR:
		return "OVR"
	case BOT:
		return "BOT"
	case DMO:
		return "DMO"
	case TEN:
		return "TEN" // TODO: Verify, this is only suspected to be the target of "TEN".
	default:
		return "UNK"
	}
}

// platformCodeFromString converts a string to its PlatformCode representation.
func platformCodeFromString(s string) (PlatformCode, bool) {
	switch s {
	case "STM":
		return STM, true
	case "PSN":
		return PSN, true
	case "XBX":
		return XBX, true
	case "OVR_ORG":
		return OVR_ORG, true
	case "OVR":
		return OVR, true
	case "BOT":
		return BOT, true
	case "DMO":
		return DMO, true
	case "TEN":
		return TEN, true
	default:
		return 0, false
	}
}
