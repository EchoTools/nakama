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
