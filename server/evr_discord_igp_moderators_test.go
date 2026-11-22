package server

import (
	"testing"

	"go.uber.org/atomic"
	"go.uber.org/zap"
)

func TestInGamePanel_ModeratorListedOnlyForOperatorAuditorAndGuildAdmin(t *testing.T) {
	for _, c := range rpcCallers(newModFixture(t)) {
		t.Run(c.name, func(t *testing.T) {
			useBotUserID(t)
			f := newModFixture(t)
			nk := inMatchNK{&moderatorNK{locationLabelNK: locationLabelNK{label: f.label}, f: f, caller: c.modCaller}}
			p := &InGamePanel{nk: nk, db: rpcDB(t, c), logger: NewRuntimeGoLogger(zap.NewNop()), userID: c.userID, selectedPlayer: atomic.NewPointer[PlayerInfo](nil)}

			label, err := p.retrieveCurrentMatchLabel(rpcContext(c), c.userID)
			if err != nil {
				t.Fatalf("retrieveCurrentMatchLabel: %v", err)
			}

			edit := p.createUpdateEdit("", label, nil)
			doc := embedsJSON(t, *edit.Embeds...)
			if edit.Components != nil {
				doc += jsonString(t, *edit.Components)
			}
			requireModeratorShownOnlyIf(t, c.name, doc, c.seesMods)
		})
	}
}
