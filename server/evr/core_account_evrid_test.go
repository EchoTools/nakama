package evr

import (
	"reflect"
	"testing"

	"github.com/gofrs/uuid/v5"
)

func TestEvrId_UUID(t *testing.T) {
	type fields struct {
		PlatformCode PlatformCode
		AccountId    uint64
	}
	tests := []struct {
		name   string
		fields fields
		want   uuid.UUID
	}{
		{
			name: "valid UUID",
			fields: fields{
				PlatformCode: 1,
				AccountId:    1,
			},
			want: uuid.FromStringOrNil("496d8944-6159-5c53-bdc8-1cab22f9d28d"),
		},
		{
			name: "invalid PlatformCode",
			fields: fields{
				PlatformCode: 0,
				AccountId:    12341234,
			},
			want: uuid.Nil,
		},
		{
			name: "invalid AccountId",
			fields: fields{
				PlatformCode: 4,
				AccountId:    0,
			},
			want: uuid.Nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			xpi := &EvrId{
				PlatformCode: tt.fields.PlatformCode,
				AccountId:    tt.fields.AccountId,
			}
			if got := xpi.UUID(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EvrId.UUID() = %v, want %v", got, tt.want)
			}
		})
	}
}
