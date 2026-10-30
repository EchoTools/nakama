package server

import (
	"fmt"
	"strings"
	"testing"
)

// An equip logged after more previews than the entry cap must survive: the
// previews are filtered out, so they must not count toward the cap.
func TestPrepareRemoteLogs_EquipAfterManyPreviewsSurvives(t *testing.T) {
	logs := make([]string, 0, 2*maxRemoteLogEntries+1)
	for i := 0; i < 2*maxRemoteLogEntries; i++ {
		logs = append(logs, fmt.Sprintf(`{"message":"Customization Item Preview","item":%d}`, i))
	}
	equip := `{"message_type":"CUSTOMIZATION_METRICS_PAYLOAD","[event_type]":"item_equipped","[item_name]":"rwd_tag_0001"}`
	logs = append(logs, equip)

	got := prepareRemoteLogs(logs)
	if len(got) != 1 || got[0] != equip {
		t.Fatalf("prepareRemoteLogs kept %d entries, want only the equip: %v", len(got), got)
	}
}

func TestPrepareRemoteLogs_CapsUnfilteredEntries(t *testing.T) {
	logs := make([]string, 0, maxRemoteLogEntries+10)
	for i := 0; i < maxRemoteLogEntries+10; i++ {
		logs = append(logs, fmt.Sprintf(`{"message":"kept","n":%d}`, i))
	}
	if got := prepareRemoteLogs(logs); len(got) != maxRemoteLogEntries {
		t.Fatalf("prepareRemoteLogs kept %d entries, want %d", len(got), maxRemoteLogEntries)
	}
}

func TestPrepareRemoteLogs_CapsPayloadSize(t *testing.T) {
	big := `{"message":"kept","pad":"` + strings.Repeat("x", maxRemoteLogPayload/2) + `"}`
	if got := prepareRemoteLogs([]string{big, big, big}); len(got) != 1 {
		t.Fatalf("prepareRemoteLogs kept %d entries, want 1", len(got))
	}
}
