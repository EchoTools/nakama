package evr

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRemoteLogCustomizationMetricsPayload_GetCategory(t *testing.T) {
	type fields struct {
		Message     string
		SessionUUID string
		PanelID     string
		EventType   string
		EventDetail string
		ItemID      int64
		ItemName    string
		UserID      string
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "Test GetCategory with Reward Item",
			fields: fields{
				ItemName: "rwd_category_item",
			},
			want: "category",
		},
		{
			name: "Test GetCategory with Standard Item",
			fields: fields{
				ItemName: "category_item",
			},
			want: "category",
		},
		{
			name: "Test goal FX category name",
			fields: fields{
				ItemName: "rwd_goal_fx_0014",
			},
			want: "goal",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &RemoteLogCustomizationMetricsPayload{
				GenericRemoteLog: GenericRemoteLog{
					MessageData: tt.fields.Message,
				},
				SessionUUIDStr: tt.fields.SessionUUID,
				PanelID:        tt.fields.PanelID,
				EventType:      tt.fields.EventType,
				EventDetail:    tt.fields.EventDetail,
				ItemID:         tt.fields.ItemID,
				ItemName:       tt.fields.ItemName,
				UserID:         tt.fields.UserID,
			}
			if got := m.GetCategory(); got != tt.want {
				t.Errorf("RemoteLogCustomizationMetricsPayload.GetCategory() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseRemoteLog(t *testing.T) {
	tests := []struct {
		name      string
		message   string
		want      any
		wantError bool
	}{
		{
			name:    "Test with valid USER_DISCONNECT message",
			message: `{"[game_info][game_time]": 36.913235, "[game_info][is_arena]": true, "[game_info][is_capture_point]": false, "[game_info][is_combat]": false, "[game_info][is_payload]": false, "[game_info][is_private]": true, "[game_info][is_social]": false, "[game_info][level]": "mpl_arena_a", "[game_info][match_type]": "Echo_Arena_Private", "[player_info][displayname]": "sprockee", "[player_info][teamid]": 1, "[player_info][userid]": "OVR-ORG-123412341234", "[session][uuid]": "{CC09F341-AF21-4BDF-AB77-1083AD1B3C1E}", "message": "User disconnected while game still playing", "message_type": "USER_DISCONNECT"}`,
			want: &RemoteLogUserDisconnected{
				GenericRemoteLog: GenericRemoteLog{
					MessageData: "User disconnected while game still playing",
					Type:        "USER_DISCONNECT",
				},
				GameInfoGameTime:       36.913235,
				GameInfoIsArena:        true,
				GameInfoIsCapturePoint: false,
				GameInfoIsCombat:       false,
				GameInfoIsPayload:      false,
				GameInfoIsPrivate:      true,
				GameInfoIsSocial:       false,
				GameInfoLevel:          "mpl_arena_a",
				GameInfoMatchType:      "Echo_Arena_Private",
				PlayerInfoDisplayname:  "sprockee",
				PlayerInfoTeamid:       1,
				PlayerXPID:             "OVR-ORG-123412341234",
				SessionUUIDStr:         "{CC09F341-AF21-4BDF-AB77-1083AD1B3C1E}",
			},
			wantError: false,
		},
		{
			name: "Test with valid CUSTOMIZATION_METRICS_PAYLOAD message",
			message: `{
						"message": "CUSTOMIZATION_METRICS_PAYLOAD",
						"[session][uuid]": "{20616A2A-ED52-43EF-93F0-4558D1147550}",
						"[panel_id]": "item_panel",
						"[event_type]": "item_equipped",
						"[event_detail]": "",
						"[item_id]": 8511708811739928018,
						"[item_name]": "rwd_tint_s2_b_default",
						"[user_id]": "OVR-ORG-1139"
					}`,
			want: &RemoteLogCustomizationMetricsPayload{
				GenericRemoteLog: GenericRemoteLog{
					MessageData: "CUSTOMIZATION_METRICS_PAYLOAD",
				},
				SessionUUIDStr: "{20616A2A-ED52-43EF-93F0-4558D1147550}",
				PanelID:        "item_panel",
				EventType:      "item_equipped",
				EventDetail:    "",
				ItemID:         8511708811739928018,
				ItemName:       "rwd_tint_s2_b_default",
				UserID:         "OVR-ORG-1139",
			},
			wantError: false,
		},

		{
			name:    "Test with valid GAME_SETTINGS message",
			message: `{"message":"Game Pause Settings","message_type":"GAME_SETTINGS","game_settings":{"announcer":1,"music":0,"sfx":8,"voip":10,"wristangleoffset":21.000000,"smoothrotationspeed":10.000000,"personalbubbleradius":2.000000,"grabdeadzone":7.000000,"releasedistance":7.500000,"personalbubblemode":0,"personalspacemode":2,"voipmode":3,"voipmodeffect":0,"voiploudnesslevel":0,"dynamicmusicmode":0,"HUD":false,"EnableYaw":true,"EnablePitch":true,"EnableRoll":false,"EnableSmoothRotation":true,"EnablePersonalBubble":false,"EnablePersonalSpace":true,"EnableNetStatusHUD":false,"EnableNetStatusPause":true,"EnableAPIAccess":true,"EnableGhostAll":false,"EnableMuteAll":false,"EnableMuteEnemyTeam":false,"MatchTagDisplay":true,"EnableVoipLoudness":true,"EnableMaxLoudness":false,"EnableStreamerMode":false}}`,
			want: &RemoteLogPauseSettings{
				GenericRemoteLog: GenericRemoteLog{
					MessageData: "Game Pause Settings",
					Type:        "GAME_SETTINGS",
				},
				Settings: GamePauseSettings{
					EnableAPIAccess:      true,
					EnableGhostAll:       false,
					EnableMaxLoudness:    false,
					EnableMuteAll:        false,
					EnableMuteEnemyTeam:  false,
					EnableNetStatusHUD:   false,
					EnableNetStatusPause: true,
					EnablePersonalBubble: false,
					EnablePersonalSpace:  true,
					EnablePitch:          true,
					EnableRoll:           false,
					EnableSmoothRotation: true,
					EnableStreamerMode:   false,
					EnableVoipLoudness:   true,
					EnableYaw:            true,
					Hud:                  false,
					MatchTagDisplay:      true,
					Announcer:            1,
					Dynamicmusicmode:     0,
					GrabDeadZone:         7.0,
					Music:                0,
					Personalbubblemode:   0,
					Personalbubbleradius: 2.0,
					Personalspacemode:    2,
					ReleaseDistance:      7.5,
					Sfx:                  8,
					Smoothrotationspeed:  10.0,
					Voip:                 10,
					Voiploudnesslevel:    0,
					Voipmode:             3,
					Voipmodeffect:        0,
					WristAngleOffset:     21.0,
				},
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			got, err := UnmarshalRemoteLog([]byte(tt.message))

			if err != nil && !tt.wantError {
				t.Errorf("ParseRemoteLog() error = %v", err)

			} else {

				if diff := cmp.Diff(tt.want, got); diff != "" {
					t.Errorf("ParseRemoteLog() = - want / + got: %s", cmp.Diff(tt.want, got))
				}
			}
		})
	}
}
