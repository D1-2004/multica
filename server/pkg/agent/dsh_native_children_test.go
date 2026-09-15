package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestDSHNativeChildFollowUsesDurableParentAddress(t *testing.T) {
	for _, mode := range []string{"one-shot", "continuable"} {
		t.Run(mode, func(t *testing.T) {
			header := json.RawMessage(`{"version":3,"id":"parent","createdAt":1,"cwd":"/work","isSeeded":false,"delegationDepth":0}`)
			childHeader := json.RawMessage(`{"version":3,"id":"child","createdAt":2,"cwd":"/work","isSeeded":false,"delegationDepth":1,"parentSession":"parent","origin":"subagent"}`)
			events := []json.RawMessage{
				nativeTestEvent(0, "multica/task-child-start", `{"requestId":"mine","parentSessionId":"parent","activationId":"activation"}`),
				nativeTestEvent(1, "tool/result", `{"text":"real child result"}`),
				nativeTestEvent(2, "multica/task-child-end", `{"requestId":"mine","parentSessionId":"parent","activationId":"activation","firstSeq":0}`),
			}
			cursor, more := int64(2), false
			reply, _ := json.Marshal(map[string]any{"ok": true, "value": dshNativeSnapshot{Type: "snapshot", Header: childHeader, Cursor: &cursor, Records: nativeTestRecords(events), HasMore: &more}})
			client, requests := dshHostPipe(t, string(reply)+"\n")
			backend := dshNativeBackend{native: DSHNativeHostConfig{SessionID: "parent", RequestID: "mine", WorkDir: "/work"}, client: client}
			refs := []json.RawMessage{nativeTestEvent(3, "multica/task-child", `{"requestId":"mine","childSessionId":"child","activationId":"activation","firstSeq":0}`)}
			children, err := backend.childTrajectories(context.Background(), header, refs, map[string]string{"child": mode})
			if err != nil || len(children) != 1 || !children[0].Scope.Closed || len(children[0].Events) != 3 {
				t.Fatalf("child artifact: %+v, %v", children, err)
			}
			address := (<-requests)["request"].(map[string]any)["address"]
			want := map[string]any{"kind": "subagent", "parentSessionId": "parent", "childSessionId": "child", "mode": mode}
			if !reflect.DeepEqual(address, want) {
				t.Fatalf("official child address = %#v, want %#v", address, want)
			}
			if _, err := backend.childTrajectories(context.Background(), header, refs, nil); err == nil {
				t.Fatal("child without catalog identity accepted")
			}
		})
	}
}

func TestDSHNativeChildPaginationKeepsParentAndMode(t *testing.T) {
	events := []json.RawMessage{nativeTestEvent(0, "sandbox/mode", `{}`), nativeTestEvent(1, "tool/result", `{}`)}
	cursor, more := int64(1), true
	snapshot := dshNativeSnapshot{Type: "snapshot", Header: json.RawMessage(`{"version":3,"id":"child","cwd":"/work","isSeeded":false,"parentSession":"parent","origin":"subagent","delegationDepth":1}`), Cursor: &cursor, Records: nativeTestRecords(events[1:]), HasMore: &more}
	raw, _ := json.Marshal(snapshot)
	called := false
	call := func(_ context.Context, method string, request any) (json.RawMessage, error) {
		called = true
		address := request.(map[string]any)["address"].(map[string]string)
		want := map[string]string{"kind": "subagent", "parentSessionId": "parent", "childSessionId": "child", "mode": "continuable"}
		if method != "page" || !reflect.DeepEqual(address, want) {
			t.Fatalf("child pagination address = %#v", request)
		}
		no := false
		return json.Marshal(dshNativePage{Records: nativeTestRecords(events[:1]), HasMore: &no})
	}
	_, got, err := readDSHNativeBaseline(context.Background(), call, raw, "child", "/work", "parent", "continuable")
	if err != nil || !called || len(got) != 2 {
		t.Fatalf("child pages: count=%d called=%t err=%v", len(got), called, err)
	}
	if _, _, err := readDSHNativeBaseline(context.Background(), call, raw, "child", "/work", "other", "continuable"); err == nil {
		t.Fatal("foreign parent accepted")
	}
}

func TestDSHNativeHistoryRetainsChildCatalogFromPreviousTask(t *testing.T) {
	history := dshNativeTaskHistory{requestID: "mine"}
	events := []json.RawMessage{nativeTestEvent(0, "subagent/catalog", `{"version":0,"childId":"child","mode":"continuable"}`)}
	events = append(events, nativeTestTurn(1, 1, "mine", "resumed child", "completed")...)
	for _, event := range events {
		if err := history.accept(event); err != nil {
			t.Fatal(err)
		}
	}
	if history.childModes["child"] != "continuable" || len(history.ownEvents) != 4 {
		t.Fatal("prior catalog must supply address identity without entering the task interval")
	}
}
