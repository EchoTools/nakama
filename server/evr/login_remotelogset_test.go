package evr

import "testing"

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
				Message:     tt.fields.Message,
				SessionUUID: tt.fields.SessionUUID,
				PanelID:     tt.fields.PanelID,
				EventType:   tt.fields.EventType,
				EventDetail: tt.fields.EventDetail,
				ItemID:      tt.fields.ItemID,
				ItemName:    tt.fields.ItemName,
				UserID:      tt.fields.UserID,
			}
			if got := m.GetCategory(); got != tt.want {
				t.Errorf("RemoteLogCustomizationMetricsPayload.GetCategory() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestXxx(t *testing.T) {
	jsondata := `{"[game_info][game_time]": 36.913235, "[game_info][is_arena]": true, "[game_info][is_capture_point]": false, "[game_info][is_combat]": false, "[game_info][is_payload]": false, "[game_info][is_private]": true, "[game_info][is_social]": false, "[game_info][level]": "mpl_arena_a", "[game_info][match_type]": "Echo_Arena_Private", "[player_info][displayname]": "lowz", "[player_info][teamid]": 1, "[player_info][userid]": "OVR-ORG-7985122688226370", "[session][uuid]": "{CC09F341-AF21-4BDF-AB77-1083AD1B3C1E}", "message": "User disconnected while game still playing", "message_type": "USER_DISCONNECT"}`
	_ = jsondata
}
