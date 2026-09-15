package dshtrajectory

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func childFixture(t *testing.T) (Document, ChildDocument) {
	t.Helper()
	root, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	root.Events[3] = json.RawMessage(`{"type":"multica/task-child","seq":43,"time":5,"data":{"requestId":"mine","childSessionId":"child","activationId":"activation","firstSeq":20}}`)
	child := ChildDocument{Scope: ChildScope{SessionID: "child", ParentSessionID: "session", ActivationID: "activation", FirstSeq: 20, LastSeq: 23, Closed: true}, Header: json.RawMessage(`{"type":"session","version":3,"id":"child","parentSession":"session","origin":"subagent","isSeeded":true,"delegationDepth":1,"createdAt":2}`), Events: []json.RawMessage{
		json.RawMessage(`{"type":"multica/task-child-start","seq":20,"time":5,"data":{"requestId":"mine","parentSessionId":"session","activationId":"activation"}}`),
		json.RawMessage(`{"type":"turn/start","seq":21,"time":5,"data":{"turn":2}}`),
		json.RawMessage(`{"type":"turn/end","seq":22,"time":6,"data":{"turn":2,"reason":{"kind":"completed"}}}`),
		json.RawMessage(`{"type":"multica/task-child-end","seq":23,"time":6,"data":{"requestId":"mine","parentSessionId":"session","activationId":"activation","firstSeq":20}}`),
	}}
	return root, child
}
func TestChildBundlePreservesNativeIdentityAndCounts(t *testing.T) {
	root, child := childFixture(t)
	data, err := Encode(root.Scope, root.Header, root.Events, child)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if doc.EventCount() != 9 || len(doc.Children) != 1 || doc.Scope.Version != 2 || !bytes.Equal(doc.Children[0].Events[0], child.Events[0]) {
		t.Fatal("child identity lost")
	}
	encoded, err := Encode(doc.Scope, doc.Header, doc.Events, doc.Children...)
	if err != nil || !bytes.Equal(encoded, data) {
		t.Fatal("bundle changed on re-encode", err)
	}
	if _, err := Encode(root.Scope, root.Header, root.Events); err == nil {
		t.Fatal("root-only export silently omitted its referenced child")
	}
}
func TestChildBundleRejectsCrossTaskMissingAndForgedRanges(t *testing.T) {
	root, child := childFixture(t)
	data, err := Encode(root.Scope, root.Header, root.Events, child)
	if err != nil {
		t.Fatal(err)
	}
	for name, invalid := range map[string]string{
		"other task":     strings.Replace(string(data), `"parentSessionId":"session","activationId":"activation"`, `"parentSessionId":"session","activationId":"other"`, 1),
		"other parent":   strings.Replace(string(data), `"parentSession":"session"`, `"parentSession":"other"`, 1),
		"missing child":  strings.Join(strings.Split(string(data), "\n")[:7], "\n"),
		"sequence gap":   strings.Replace(string(data), `"seq":21`, `"seq":99`, 1),
		"unclosed lie":   strings.Replace(string(data), `"closed":true`, `"closed":false`, 1),
		"missing closed": strings.Replace(string(data), `,"closed":true`, "", 1),
		"null first":     strings.Replace(string(data), `"firstSeq":40`, `"firstSeq":null`, 1),
		"extra interval": string(data) + string(child.Header) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(invalid)); err == nil {
				t.Fatal("invalid bundle accepted")
			}
		})
	}
}
func TestChildBundlePreservesExplicitInterruptedPrefix(t *testing.T) {
	root, child := childFixture(t)
	child.Events = child.Events[:3]
	child.Scope.LastSeq--
	child.Scope.Closed = false
	data, err := Encode(root.Scope, root.Header, root.Events, child)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(data)
	if err != nil || doc.Children[0].Scope.Closed || doc.EventCount() != 8 {
		t.Fatal("interrupted prefix lost", err)
	}
}
func TestChildBundleIncludesNestedLineage(t *testing.T) {
	root, child := childFixture(t)
	child.Events[1] = json.RawMessage(`{"type":"multica/task-child","seq":21,"time":5,"data":{"requestId":"mine","childSessionId":"grandchild","activationId":"nested","firstSeq":20}}`)
	grandchild := child
	grandchild.Scope.SessionID = "grandchild"
	grandchild.Scope.ParentSessionID = "child"
	grandchild.Scope.ActivationID = "nested"
	grandchild.Header = json.RawMessage(`{"type":"session","version":3,"id":"grandchild","parentSession":"child","origin":"subagent","isSeeded":true,"delegationDepth":2,"createdAt":2}`)
	grandchild.Events = make([]json.RawMessage, len(child.Events))
	for i, raw := range child.Events {
		grandchild.Events[i] = json.RawMessage(strings.ReplaceAll(strings.ReplaceAll(string(raw), `"parentSessionId":"session"`, `"parentSessionId":"child"`), `"activationId":"activation"`, `"activationId":"nested"`))
	}
	grandchild.Events[1] = json.RawMessage(`{"type":"turn/start","seq":21,"time":5,"data":{"turn":2}}`)
	data, err := Encode(root.Scope, root.Header, root.Events, child, grandchild)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(data)
	if err != nil || len(doc.Children) != 2 {
		t.Fatal("nested child missing", err)
	}
	if _, err := Encode(root.Scope, root.Header, root.Events, grandchild, child); err == nil {
		t.Fatal("wrong parent order accepted")
	}
}
