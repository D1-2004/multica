package dws_test

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/pkg/dws"
	"github.com/multica-ai/multica/server/pkg/dws/clicompat"
)

func TestA2UISendArgumentsMatchCLI(t *testing.T) {
	messages := []string{`{"version":"v1.0","createSurface":{"surfaceId":"s-1"}}`, `{"version":"v1.0","updateComponents":{}}`}
	got, err := dws.A2UISendArguments(dws.A2UISend{
		Target:    dws.Target{ConversationID: " cid "},
		Messages:  messages,
		Summary:   " 确认 ",
		BizCardID: " card-1 ",
		RequestID: " req-1 ",
	})
	if err != nil {
		t.Fatal(err)
	}
	want, fail := clicompat.A2UISendArgs(clicompat.A2UISendFlags{
		ChatID: "cid", BizCardID: "card-1", RequestID: "req-1", Summary: "确认", Messages: messages,
	})
	if fail != nil {
		t.Fatal(fail)
	}
	if !jsonEqual(t, got, want) {
		t.Fatalf("send args = %#v\ncli = %#v", got, want)
	}

	got, err = dws.A2UISendArguments(dws.A2UISend{
		Target:    dws.Target{UserOpenDingTalkID: "DAAAAAAAAAAAiE"},
		Messages:  messages,
		Summary:   "确认",
		BizCardID: "card-1",
		RequestID: "req-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	want, fail = clicompat.A2UISendArgs(clicompat.A2UISendFlags{
		OpenDingTalkID: "DAAAAAAAAAAAiE", BizCardID: "card-1", RequestID: "req-1", Summary: "确认", Messages: messages,
	})
	if fail != nil {
		t.Fatal(fail)
	}
	if !jsonEqual(t, got, want) {
		t.Fatalf("direct send args = %#v\ncli = %#v", got, want)
	}
}

func TestA2UIFinishArgumentsMatchCLI(t *testing.T) {
	got, err := dws.A2UIFinishArguments("transformer_card_abc", "s-1", "req-finish")
	if err != nil {
		t.Fatal(err)
	}
	messages, _ := got["a2uiMessages"].([]string)
	want, fail := clicompat.UpdateA2UIArgs("transformer_card_abc", "FINISH", messages, nil)
	if fail != nil {
		t.Fatal(fail)
	}
	delete(want, "requestId")
	delete(got, "requestId")
	if !jsonEqual(t, got, want) {
		t.Fatalf("finish args = %#v\ncli = %#v", got, want)
	}
}

func TestA2UISendRejectsTwoTargets(t *testing.T) {
	_, err := dws.A2UISendArguments(dws.A2UISend{
		Target:   dws.Target{ConversationID: "cid", UserOpenDingTalkID: "DAAAAAAAAAAAiE"},
		Messages: []string{"{}"},
		Summary:  "确认",
	})
	if err == nil {
		t.Fatal("two targets were accepted")
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	left, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	right, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(left) == string(right)
}
