package service

import (
	"fmt"
	"strings"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/muesli/reflow/wordwrap"
	"google.golang.org/grpc/status"
)

type LoginError struct {
	Tag     string
	Message string
}

func (e *LoginError) Error() string {
	return fmt.Sprintf("%s: %s", e.Tag, e.Message)
}

func newLoginError(tag, message string, v ...any) error {
	return &LoginError{
		Tag:     tag,
		Message: fmt.Sprintf(message, v...),
	}
}

func formatLoginErrorMessage(xpID evr.XPID, discordID string, err error) string {
	errContent := ""
	if e, ok := status.FromError(err); ok {
		errContent = e.Message()
	} else {
		errContent = err.Error()
	}

	// Format the error message with the XPID prefix
	if discordID == "" {
		errContent = fmt.Sprintf("[%s]\n %s", xpID.String(), errContent)
	} else {
		errContent = fmt.Sprintf("[XPID:%s / Discord:%s]\n %s", xpID.String(), discordID, errContent)
	}

	// Replace ": " with ":\n" for better readability
	errContent = strings.Replace(errContent, ": ", ":\n", 2)

	// Word wrap the error message
	errContent = wordwrap.String(errContent, 60)

	return errContent
}

// DeviceNotLinkedError is returned when a user tries to log in with a device that is not linked to their account.
type DeviceNotLinkedError struct {
	Code         string
	Instructions string
}

func (e *DeviceNotLinkedError) Error() string {
	return fmt.Sprintf("Your Code is: >>> %s <<<\n%s", e.Code, e.Instructions)
}

type AccountDisabledError struct {
	message   string
	reportURL string
}

func (e AccountDisabledError) Error() string {
	return strings.Join([]string{
		"Account disabled by EchoVRCE Admins",
		e.message,
		"Report issues at " + e.reportURL,
	}, "\n")
}

func (e AccountDisabledError) Is(target error) bool {
	_, ok := target.(AccountDisabledError)
	return ok
}

type NewLocationError struct {
	guildName   string
	code        string
	botUsername string
	useDMs      bool
}

func (e NewLocationError) Error() string {
	if e.useDMs {
		// DM was successful
		return strings.Join([]string{
			"Please authorize this new location.",
			fmt.Sprintf("Check your Discord DMs from @%s.", e.botUsername),
			fmt.Sprintf("Select code >>> %s <<<", e.code),
		}, "\n")
	} else if e.guildName != "" {
		// DMs were blocked, Use the guild name if it's available
		return strings.Join([]string{
			"Authorize new location:",
			fmt.Sprintf("Go to %s, type /verify", e.guildName),
			fmt.Sprintf("When prompted, select code >>> %s <<<", e.code),
		}, "\n")
	} else if e.botUsername != "" {
		// DMs were blocked, Use the bot username if it's available
		return strings.Join([]string{
			"Authorize this new location by typing:",
			"/verify",
			fmt.Sprintf("and select code >>> %s <<< in a guild with the @%s bot.", e.code, e.botUsername),
		}, "\n")
	} else {
		// DMs were blocked, but no bot username was provided
		return "New location detected. Please contact EchoVRCE support."
	}
}
