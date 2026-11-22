package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

type statusPresence struct {
	runtime.Presence
	status string
}

func (p statusPresence) GetStatus() string { return p.status }

// inMatchNK is a moderatorNK whose caller is in the fixture's match.
type inMatchNK struct {
	*moderatorNK
}

func (n inMatchNK) StreamUserList(mode uint8, subject, subcontext, label string, includeHidden, includeNotHidden bool) ([]runtime.Presence, error) {
	return []runtime.Presence{statusPresence{status: n.f.label.ID.String()}}, nil
}

func requireModeratorShownOnlyIf(t *testing.T, name, rendered string, sees bool) {
	t.Helper()
	if got := strings.Contains(rendered, modTestModeratorTag); got != sees {
		t.Errorf("%s: moderator listed = %v, want %v: %s", name, got, sees, rendered)
	}
	if !strings.Contains(rendered, "BluePlayer") {
		t.Errorf("%s: a player is missing: %s", name, rendered)
	}
}

func TestWhereAmI_ModeratorListedOnlyForOperatorAuditorAndGuildAdmin(t *testing.T) {
	for _, c := range rpcCallers(newModFixture(t)) {
		t.Run(c.name, func(t *testing.T) {
			useBotUserID(t)
			f := newModeratorEmbedFixture(t)
			f.label.GroupID = nil
			f.label.GameServer.OperatorID = uuid.Nil
			nk := inMatchNK{&moderatorNK{locationLabelNK: locationLabelNK{label: f.label}, f: f, caller: c.modCaller}}
			d := &DiscordAppBot{nk: nk, db: rpcDB(t, c), guildGroupRegistry: NewGuildGroupRegistry(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nk, nil)}

			data, err := d.getWhereAmIData(rpcContext(c), NewRuntimeGoLogger(zap.NewNop()), c.userID, "")
			if err != nil || data == nil {
				t.Fatalf("getWhereAmIData: %v, %v", data, err)
			}

			wantPlayers := len(f.label.Players) - 2
			if c.seesMods {
				wantPlayers = len(f.label.Players)
			}
			if len(data.Players) != wantPlayers {
				t.Errorf("%s: %d players, want %d", c.name, len(data.Players), wantPlayers)
			}
			requireModeratorShownOnlyIf(t, c.name, embedsJSON(t, d.createWhereAmIEmbed(data), d.createServerInfoEmbed(data)), c.seesMods)
		})
	}
}

func TestShutdownMatchEmbed_ModeratorListedOnlyForOperatorAuditorAndGuildAdmin(t *testing.T) {
	for _, c := range rpcCallers(newModFixture(t)) {
		t.Run(c.name, func(t *testing.T) {
			useBotUserID(t)
			f := newModFixture(t)
			nk := &moderatorNK{locationLabelNK: locationLabelNK{label: f.label}, f: f, caller: c.modCaller}
			d := &DiscordAppBot{nk: nk, db: rpcDB(t, c), guildGroupRegistry: NewGuildGroupRegistry(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nk, nil)}

			label := labelForViewer(rpcContext(c), NewRuntimeGoLogger(zap.NewNop()), d.db, nk, c.userID, f.label)

			requireModeratorShownOnlyIf(t, c.name, embedsJSON(t, d.createShutdownMatchEmbed(label)), c.seesMods)
		})
	}
}

func jsonString(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}
