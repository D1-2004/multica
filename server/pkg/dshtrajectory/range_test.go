package dshtrajectory

import (
	"bytes"
	"strings"
	"testing"
)

const fixture = `{"type":"multica/task-trajectory","version":1,"sessionId":"session","requestId":"mine","firstSeq":40,"lastSeq":44}
{"type":"session","version":3,"id":"session","createdAt":1,"isSeeded":false}
{"type":"turn/start","seq":40,"time":2,"data":{"turn":9}}
{"type":"user/message","seq":41,"time":3,"data":{"source":{"kind":"plugin"},"content":"current context"}}
{"type":"user/message","seq":42,"time":4,"data":{"source":{"kind":"user","rpcId":"mine"},"content":"task prompt"}}
{"type":"assistant/message","seq":43,"time":5,"data":{"message":{"content":[]}}}
{"type":"turn/end","seq":44,"time":6,"data":{"turn":9,"reason":{"kind":"completed"}}}
`

func TestTaskRangePreservesNativeEventsAndExplicitScope(t *testing.T) {
	doc, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Scope.FirstSeq != 40 || len(doc.Events) != 5 || !bytes.Contains(doc.Events[1], []byte("current context")) {
		t.Fatal("native turn prefix lost")
	}
	encoded, err := Encode(doc.Scope, doc.Header, doc.Events)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Parse(encoded)
	if err != nil || restored.Scope != doc.Scope {
		t.Fatalf("scope changed: %v", err)
	}
	for i := range doc.Events {
		if !bytes.Equal(doc.Events[i], restored.Events[i]) {
			t.Fatal("native event changed")
		}
	}
}

func TestTaskRangeKeepsPluginPayloadsWithTheirOwnSchemas(t *testing.T) {
	for _, replacement := range []string{
		`{"type":"request/header","seq":43,"time":5,"data":{"reason":"initial","header":{"config":{"provider":"multica"}}}}`,
		`{"type":"approval/policy","seq":43,"time":5,"data":{"source":"delegation","policy":"never"}}`,
	} {
		lines := strings.Split(fixture, "\n")
		lines[5] = replacement
		if _, err := Parse([]byte(strings.Join(lines, "\n"))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTaskRangeRejectsUnscopedMixedAndIncompleteEvents(t *testing.T) {
	for name, invalid := range map[string]string{
		"wrong request":     strings.Replace(fixture, `"rpcId":"mine"`, `"rpcId":"other"`, 1),
		"wrong session":     strings.Replace(fixture, `"id":"session"`, `"id":"other"`, 1),
		"sequence gap":      strings.Replace(fixture, `"seq":41`, `"seq":99`, 1),
		"partial tail":      strings.Replace(fixture, `"type":"turn/end"`, `"type":"assistant/message"`, 1),
		"missing first seq": strings.Replace(fixture, `"firstSeq":40,`, "", 1),
		"null first seq":    strings.Replace(fixture, `"firstSeq":40`, `"firstSeq":null`, 1),
		"another turn":      strings.Replace(fixture, `"turn":9,"reason"`, `"turn":8,"reason"`, 1),
		"mixed request":     strings.Replace(fixture, `"kind":"plugin"`, `"kind":"user","rpcId":"other"`, 1),
		"duplicate request": strings.Replace(fixture, `"kind":"plugin"`, `"kind":"user","rpcId":"mine"`, 1),
		"child":             strings.Replace(fixture, `"isSeeded":false`, `"isSeeded":false,"parentSession":"parent","delegationDepth":1`, 1),
		"unknown reason":    strings.Replace(fixture, `"kind":"completed"`, `"kind":"accepted"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(invalid)); err == nil {
				t.Fatal("invalid scoped export accepted")
			}
		})
	}
}
