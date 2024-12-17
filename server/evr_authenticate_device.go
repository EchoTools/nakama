package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// The data used to generate the Device ID authentication string.
type DeviceAuth struct {
	AppID           uint64    // The application ID for the game
	EvrID           evr.EvrId // The xplatform ID string
	HMDSerialNumber string    // The HMD serial number
	ClientIP        string    // The client address
}

func NewDeviceAuth(appID uint64, evrID evr.EvrId, hmdSerialNumber, clientAddr string) *DeviceAuth {
	return &DeviceAuth{
		AppID:           appID,
		EvrID:           evrID,
		HMDSerialNumber: hmdSerialNumber,
		ClientIP:        clientAddr,
	}
}

// Generate the string used for device authentication.
// WARNING: If this is changed, then device "links" will be invalidated.
func (d DeviceAuth) Token() string {
	components := []string{
		strconv.FormatUint(d.AppID, 10),
		d.EvrID.Token(),
		d.HMDSerialNumber,
		d.ClientIP,
	}
	return invalidCharsRegex.ReplaceAllString(strings.Join(components, ":"), "")
}

func (d DeviceAuth) WildcardToken() string {
	d.ClientIP = "*"
	return d.Token()
}

// ParseDeviceAuthToken parses a device ID token into its components.
func ParseDeviceAuthToken(token string) (*DeviceAuth, error) {
	const minTokenLength = 8
	const expectedParts = 4

	if token == "" {
		return nil, errors.New("empty device ID token")
	}
	if len(token) < minTokenLength {
		return nil, fmt.Errorf("token too short: %s", token)
	}
	parts := strings.SplitN(token, ":", expectedParts)
	if len(parts) != expectedParts {
		return nil, fmt.Errorf("invalid device ID token: %s", token)
	}

	appID, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid app ID in device ID token: %s", token)
	}

	evrID, err := evr.ParseEvrId(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid xplatform ID in device ID token: %s", token)
	}
	hmdsn := parts[2]
	if strings.Contains(hmdsn, ":") {
		return nil, fmt.Errorf("invalid HMD serial number in device ID token: %s", token)
	}

	clientAddr := parts[3]

	return &DeviceAuth{
		AppID:           appID,
		EvrID:           *evrID,
		HMDSerialNumber: hmdsn,
		ClientIP:        clientAddr,
	}, nil
}

func (d DeviceAuth) MarshalText() string {
	return d.Token()
}

func (d *DeviceAuth) UnmarshalText(text string) error {
	deviceAuth, err := ParseDeviceAuthToken(text)
	if err != nil {
		return err
	}

	d.AppID = deviceAuth.AppID
	d.EvrID = deviceAuth.EvrID
	d.HMDSerialNumber = deviceAuth.HMDSerialNumber
	d.ClientIP = deviceAuth.ClientIP

	return nil
}
