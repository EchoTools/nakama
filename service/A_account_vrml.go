package service

import (
	"fmt"

	"github.com/echotools/vrmlgo/v5"
)

const (
	StorageKeyVRMLAccount = "VRMLAccount"
)

type VRMLAccountData struct {
	User   *vrmlgo.Member `json:"user"`
	Player *vrmlgo.Player `json:"player"`
}

type AccountAlreadyLinkedError struct {
	OwnerUserID string
}

func (e *AccountAlreadyLinkedError) Error() string {
	return fmt.Sprintf("VRML Account is already linked to user: `%s`", e.OwnerUserID)
}
