package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDaemonTaskIgnoresLegacyDingTalkIdentityMarker(t *testing.T) {
	var task Task
	if err := json.Unmarshal([]byte(`{"dingtalk_dws_identity_unavailable":true}`), &task); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "dingtalk_dws_identity_unavailable") {
		t.Fatalf("legacy DingTalk identity marker remained in daemon task contract: %s", encoded)
	}
}
