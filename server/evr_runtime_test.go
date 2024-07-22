package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	_ "google.golang.org/protobuf/proto"
)

func TestCheckGroupMembershipByName_SendsCorrectSQL(t *testing.T) {
	testDB := NewDB(t)

	type args struct {
		ctx       context.Context
		userID    string
		groupName string
		groupType string
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			"Sends correct SQL",
			args{
				context.Background(),
				uuid.Nil.String(),
				"groupName",
				"system",
			},
			false,
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CheckGroupMembershipByName(tt.args.ctx, testDB, tt.args.userID, tt.args.groupName, tt.args.groupType)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckGroupMembershipByName() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("CheckGroupMembershipByName() = %v, want %v", got, tt.want)
			}
		})
	}
}
