package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/pkg/dshtrajectory"
	"testing"
)

func nativeTestEvent(seq int, kind, data string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":%q,"seq":%d,"time":1789380708186,"data":%s}`, kind, seq, data))
}
func nativeTestTurn(start, turn int, request, text, reason string) []json.RawMessage {
	return []json.RawMessage{
		nativeTestEvent(start, "turn/start", fmt.Sprintf(`{"turn":%d}`, turn)),
		nativeTestEvent(start+1, "user/message", fmt.Sprintf(`{"source":{"kind":"user","rpcId":%q},"content":[{"type":"text","text":"fixture"}]}`, request)),
		nativeTestEvent(start+2, "assistant/message", fmt.Sprintf(`{"message":{"content":[{"type":"text","text":%q}]}}`, text)),
		nativeTestEvent(start+3, "turn/end", fmt.Sprintf(`{"turn":%d,"reason":{"kind":%q}}`, turn, reason)),
	}
}
func nativeTestRecords(events []json.RawMessage) []dshNativeRecord {
	r := []dshNativeRecord{}
	for _, e := range events {
		r = append(r, dshNativeRecord{Type: "event", Event: e})
	}
	return r
}

func TestDSHNativeBaselineOfficialRootHeaderOmitsDepth(t *testing.T) {
	for _, item := range []struct {
		suffix string
		valid  bool
	}{
		{"", true}, {`,"delegationDepth":0`, true}, {`,"delegationDepth":null`, false},
		{`,"delegationDepth":1`, false}, {`,"origin":"subagent"`, false}, {`,"parentSession":"parent"`, false},
	} {
		raw := json.RawMessage(`{"type":"snapshot","header":{"version":3,"id":"session","cwd":"/persistent/session","isSeeded":false` + item.suffix + `},"cursor":-1,"records":[],"hasMore":false}`)
		_, _, err := readDSHNativeBaseline(context.Background(), nil, raw, "session", "/persistent/session")
		if (err == nil) != item.valid {
			t.Fatalf("header %s valid=%t error=%v", item.suffix, item.valid, err)
		}
	}
}
func TestDSHNativeTaskHistoryCorrelatesItsOwnTerminalAcrossReplay(t *testing.T) {
	events := nativeTestTurn(0, 1, "old", "old result", "completed")
	events = append(events, nativeTestTurn(4, 2, "mine", "my result", "completed")...)
	events = append(events, nativeTestTurn(8, 3, "later", "other result", "completed")...)
	h := dshNativeTaskHistory{requestID: "mine"}
	for i, event := range events {
		if err := h.accept(event); err != nil {
			t.Fatal(err)
		}
		if i < 7 {
			if _, err := h.result("session"); err == nil {
				t.Fatalf("completed without owned turn/end at seq %d", i)
			}
		}
	}
	result, err := h.result("session")
	if err != nil || result.Status != "completed" || result.Output != "my result" {
		t.Fatalf("wrong result: %+v, %v", result, err)
	}
	for _, reason := range []string{"aborted", "error", "interrupted"} {
		h := dshNativeTaskHistory{requestID: "mine"}
		for _, event := range nativeTestTurn(0, 1, "mine", "partial", reason) {
			if err := h.accept(event); err != nil {
				t.Fatal(err)
			}
		}
		result, err := h.result("session")
		if err != nil || result.Status == "completed" {
			t.Fatalf("false completion for %s: %+v", reason, result)
		}
	}
}

func TestDSHNativeTaskHistoryKeepsPluginContextInOwnedTurn(t *testing.T) {
	h := dshNativeTaskHistory{requestID: "mine"}
	events := []json.RawMessage{
		nativeTestEvent(0, "turn/start", `{"turn":1}`),
		nativeTestEvent(1, "user/message", `{"source":{"kind":"plugin","plugin":"agent-instructions"}}`),
		nativeTestEvent(2, "user/message", `{"source":{"kind":"user","rpcId":"mine"}}`),
		nativeTestEvent(3, "user/message", `{"source":{"kind":"skill-catalog","form":"catalog","entries":[]}}`),
		nativeTestEvent(4, "assistant/message", `{"message":{"content":[{"type":"text","text":"mine"}]}}`),
		nativeTestEvent(5, "turn/end", `{"turn":1,"reason":{"kind":"completed"}}`),
		nativeTestEvent(6, "assistant/message", `{"message":{"content":[{"type":"text","text":"outside turn"}]}}`),
	}
	for _, event := range events {
		if err := h.accept(event); err != nil {
			t.Fatal(err)
		}
	}
	result, err := h.result("session")
	if err != nil || result.Output != "mine" {
		t.Fatalf("plugin context changed task ownership: %+v %v", result, err)
	}
	artifact, err := h.trajectory(json.RawMessage(`{"version":3,"id":"session","cwd":"/persistent/session","createdAt":1,"isSeeded":false}`), "session")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dshtrajectory.Parse(artifact)
	if err != nil || doc.Scope.FirstSeq != 0 || doc.Scope.LastSeq != 5 || len(doc.Events) != 6 {
		t.Fatalf("owned trajectory lost context or included the next task: %v", err)
	}
}
func TestDSHNativeTaskHistoryRejectsMixedDuplicateAndBrokenLogs(t *testing.T) {
	for name, events := range map[string][]json.RawMessage{
		"gap":               {nativeTestEvent(1, "turn/start", `{"turn":1}`)},
		"missing sequence":  {json.RawMessage(`{"type":"turn/start","time":1,"data":{"turn":1}}`)},
		"out of turn":       {nativeTestEvent(0, "user/message", `{"source":{"kind":"user","rpcId":"mine"}}`)},
		"wrong turn end":    {nativeTestEvent(0, "turn/start", `{"turn":1}`), nativeTestEvent(1, "turn/end", `{"turn":2,"reason":{"kind":"completed"}}`)},
		"mixed turn":        {nativeTestEvent(0, "turn/start", `{"turn":1}`), nativeTestEvent(1, "user/message", `{"source":{"kind":"user","rpcId":"mine"}}`), nativeTestEvent(2, "user/message", `{"source":{"kind":"user","rpcId":"other"}}`)},
		"duplicate request": append(nativeTestTurn(0, 1, "mine", "first", "completed"), nativeTestTurn(4, 2, "mine", "second", "completed")...),
	} {
		t.Run(name, func(t *testing.T) {
			h := dshNativeTaskHistory{requestID: "mine"}
			for _, event := range events {
				if h.accept(event) != nil {
					return
				}
			}
			t.Fatal("invalid native log accepted")
		})
	}
}
func TestDSHNativeBaselinePagesAtFrozenCursorBeforeDecidingRetry(t *testing.T) {
	events := append(nativeTestTurn(0, 1, "mine", "persisted result", "completed"), nativeTestTurn(4, 2, "later", "unrelated result", "completed")...)
	cursor := int64(7)
	more := true
	snapshot := dshNativeSnapshot{Type: "snapshot", Header: json.RawMessage(`{"version":3,"id":"session","cwd":"/persistent/session","isSeeded":false,"delegationDepth":0}`), Cursor: &cursor, Records: nativeTestRecords(events[4:]), HasMore: &more}
	raw, _ := json.Marshal(snapshot)
	called := 0
	call := func(ctx context.Context, method string, request any) (json.RawMessage, error) {
		called++
		m := request.(map[string]any)
		if method != "page" || m["throughSeq"] != int64(7) || m["beforeSeq"] != int64(4) {
			t.Fatalf("invalid page cut: %v", request)
		}
		no := false
		return json.Marshal(dshNativePage{Records: nativeTestRecords(events[:4]), HasMore: &no})
	}
	_, baseline, err := readDSHNativeBaseline(context.Background(), call, raw, "session", "/persistent/session")
	if err != nil || called != 1 || len(baseline) != 8 {
		t.Fatalf("incomplete baseline: count=%d calls=%d err=%v", len(baseline), called, err)
	}
	h := dshNativeTaskHistory{requestID: "mine"}
	for _, event := range baseline {
		if err := h.accept(event); err != nil {
			t.Fatal(err)
		}
	}
	result, err := h.result("session")
	if err != nil || result.Output != "persisted result" {
		t.Fatalf("older request lost across pagination: %+v %v", result, err)
	}
	for _, wrong := range []struct{ session, cwd string }{{"other", "/persistent/session"}, {"session", "/other"}} {
		if _, _, err := readDSHNativeBaseline(context.Background(), call, raw, wrong.session, wrong.cwd); err == nil {
			t.Fatal("adopted wrong native identity")
		}
	}
	// A server that ends pagination early cannot turn absence in its last page
	// into permission to re-submit an already completed request.
	no := false
	snapshot.HasMore = &no
	raw, _ = json.Marshal(snapshot)
	if _, _, err := readDSHNativeBaseline(context.Background(), call, raw, "session", "/persistent/session"); err == nil {
		t.Fatal("incomplete prefix accepted")
	}
}
