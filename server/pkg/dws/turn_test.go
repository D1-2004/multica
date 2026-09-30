package dws

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

var origin = Origin{ConversationID: "cid-o", MessageID: "m-o", SenderOpenDingTalkID: "s-o"}

func TestTurnAckProgressFinal(t *testing.T) {
	g, c := newTestClient(t, standardGateway)
	turn := c.Turn(origin)
	ctx := context.Background()
	if err := turn.Ack(ctx, "思考中"); err != nil {
		t.Fatal(err)
	}
	if err := turn.Progress(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := turn.Progress(ctx, "a\nb"); err != nil {
		t.Fatal(err)
	}
	res, err := turn.Final(ctx, FinalMessage{Text: "done"})
	if err != nil || res.Via != "card" || res.Handle != "card-1" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	want := []string{"mark_message_read", "add_emoji_reaction", "create_and_send_card", "update_streaming_card",
		"update_streaming_card", "update_streaming_card", "remove_emoji_reaction"}
	if got := g.tools(); !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %v", got)
	}
	if g.calls[4].Args["msgContent"] != "a\nb" || g.calls[5].Args["flowStatus"] != "3" {
		t.Fatalf("updates: %v %v", g.calls[4].Args, g.calls[5].Args)
	}
	if st := turn.State(); st.Handle != "" || len(st.Reactions) != 0 {
		t.Fatalf("a finished turn is reset: %+v", st)
	}
}

func TestTurnFinalWithoutCardQuotesTheOrigin(t *testing.T) {
	g, c := newTestClient(t, standardGateway)
	res, err := c.Turn(origin).Final(context.Background(), FinalMessage{Text: "x"})
	if err != nil || res.Via != "reply" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	content := mustJSON(t, g.call("send_personal_message").Args["content"].(string))
	if content["referenceOpenMessageId"] != "m-o" || content["srcMsgSendOpenDingTalkId"] != "s-o" {
		t.Fatalf("content = %v", content)
	}
	_, c2 := newTestClient(t, standardGateway)
	if res, _ := c2.Turn(Origin{ConversationID: "cid-o"}).Final(context.Background(), FinalMessage{Text: "x"}); res.Via != "send" {
		t.Fatalf("without an origin message: %+v", res)
	}
}

func TestTurnFinalFailedClosesTheCardAsFailed(t *testing.T) {
	g, c := newTestClient(t, standardGateway)
	turn := c.ResumeTurn(origin, TurnState{Handle: "card-9"}) // restored by a stateless caller
	if _, err := turn.Final(context.Background(), FinalMessage{Text: "x", Failed: true}); err != nil {
		t.Fatal(err)
	}
	if args := g.call("update_streaming_card").Args; args["flowStatus"] != "5" || args["bizId"] != "card-9" {
		t.Fatalf("args = %v", args)
	}
}

func TestTurnProgressKeepsTheCardWhenTheFirstUpdateFails(t *testing.T) {
	g, c := newTestClient(t, func(tool string, args map[string]any) (int, string) {
		if tool == "update_streaming_card" {
			return 503, "busy"
		}
		return standardGateway(tool, args)
	})
	turn := c.Turn(origin)
	if err := turn.Progress(context.Background(), "a"); err == nil {
		t.Fatal("want the update error")
	}
	if turn.State().Handle != "card-1" {
		t.Fatal("the created card must stay reachable")
	}
	_ = turn.Progress(context.Background(), "a b")
	count := 0
	for _, tool := range g.tools() {
		if tool == "create_and_send_card" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("cards created = %d", count)
	}
}

func TestTurnCleanupFailureIsReportedNotFatal(t *testing.T) {
	_, c := newTestClient(t, func(tool string, args map[string]any) (int, string) {
		if tool == "remove_emoji_reaction" {
			return 500, "down"
		}
		return standardGateway(tool, args)
	})
	turn := c.ResumeTurn(origin, TurnState{Handle: "card-1", Reactions: []string{"思考中"}})
	res, err := turn.Final(context.Background(), FinalMessage{Text: "x"})
	if err != nil || !strings.Contains(res.CleanupError, "思考中") || !reflect.DeepEqual(turn.State().Reactions, []string{"思考中"}) {
		t.Fatalf("res=%+v err=%v state=%+v", res, err, turn.State())
	}
}

func TestOriginOverride(t *testing.T) {
	if got := origin.Override("", "", ""); got != origin {
		t.Fatalf("no override: %+v", got)
	}
	if got := origin.Override("cid-other", "", ""); got.MessageID != "" || got.SenderOpenDingTalkID != "" {
		t.Fatalf("another conversation must not borrow origin ids: %+v", got)
	}
	if got := origin.Override("", "m-x", ""); got.ConversationID != "cid-o" || got.SenderOpenDingTalkID != "" {
		t.Fatalf("another message must not borrow the origin sender: %+v", got)
	}
}

// A backend holds a Turn for a task and saves its state while another
// goroutine reports progress (go test -race).
func TestTurnStateIsSafeDuringProgress(t *testing.T) {
	_, c := newTestClient(t, standardGateway)
	turn := c.Turn(origin)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			_ = turn.Progress(context.Background(), "step")
		}
	}()
	for i := 0; i < 20; i++ {
		_ = turn.State()
	}
	<-done
	if turn.State().Handle == "" {
		t.Fatal("no card recorded")
	}
}
