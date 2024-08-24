package server

import (
	"testing"
)

func TestDiscordAppBot_handleMemberUpdate(t *testing.T) {

	nickUpdate := `{"guild_id":"1216923249615835156","joined_at":"2024-03-12T01:38:32.081Z","nick":"sprockeeeee","deaf":false,"mute":false,"avatar":"","user":{"id":"695081603180789771","email":"","username":"sprockee","avatar":"0d2cf2e3e861a9491894bde54a81a54b","locale":"","discriminator":"0","global_name":"","token":"","verified":false,"mfa_enabled":false,"banner":"","accent_color":0,"bot":false,"public_flags":0,"premium_type":0,"system":false,"flags":0},"roles":["1219310337392771273","1251255346903781427","1251173247979094028","1251173191473168558"],"premium_since":null,"flags":0,"pending":false,"permissions":"0","communication_disabled_until":null}	`
	_ = nickUpdate
}
