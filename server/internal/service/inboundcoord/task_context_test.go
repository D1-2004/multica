package inboundcoord

import (
	"encoding/json"
	"testing"
)

func TestIndependentIssueTaskContext(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"dispatch_source":{"platform":"dingtalk","type":"digital_employee"},
		"dispatch_domain":"channel",
		"dispatch_type":"message.created",
		"dispatch_outbound":{"mode":"dws","replyTo":"latest_message"},
		"dispatch_surface":{"type":"chat"},
		"completion_callback":{"url":"/api/v1/dispatch-tasks/router-task/execution-result"},
		"dispatch_event_data":{"conversation":{"openConversationId":"cid-requester"}},
		"external_identity":{"dws":{"uid":"current-user"}}
	}`)
	encoded, err := IndependentIssueTaskContext(raw, CoordinatorIssueTriggerComment)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["completion_callback"]; present {
		t.Fatal("consumed Router callback survived in the independent Issue task")
	}
	for _, key := range []string{"dispatch_event_data", "external_identity"} {
		if _, present := got[key]; !present {
			t.Fatalf("independent Issue task lost %s", key)
		}
	}
	var surface struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(got["dispatch_surface"], &surface); err != nil {
		t.Fatal(err)
	}
	if surface.Type != "issue" || string(got[coordinatorIssueFollowUpContextKey]) != "true" {
		t.Fatalf("surface=%q follow_up=%s", surface.Type, got[coordinatorIssueFollowUpContextKey])
	}
	if string(got[coordinatorIssueTriggerContextKey]) != `"issue_comment"` {
		t.Fatalf("trigger=%s", got[coordinatorIssueTriggerContextKey])
	}
	var source struct {
		Platform string `json:"platform"`
		Type     string `json:"type"`
	}
	if err := json.Unmarshal(got["dispatch_source"], &source); err != nil {
		t.Fatal(err)
	}
	if source.Platform != "dingtalk" || source.Type != "digital_employee" {
		t.Fatalf("digital-employee source overwritten: %+v", source)
	}
}

func TestIndependentIssueTaskContextStampsRobotEnvelopeForStream(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"dingtalk_stream_source":{"hostname":"host","node_id":"node","connection_id":"conn"},
		"external_identity":{"dws":{"uid":"current-user"}}
	}`)
	encoded, err := IndependentIssueTaskContext(raw, CoordinatorIssueTriggerCreate)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	var source struct {
		Platform string `json:"platform"`
		Type     string `json:"type"`
	}
	if err := json.Unmarshal(got["dispatch_source"], &source); err != nil {
		t.Fatal(err)
	}
	if source.Platform != "dingtalk" || source.Type != "robot" {
		t.Fatalf("stream robot source=%+v", source)
	}
	if string(got["dispatch_domain"]) != `"channel"` || string(got["dispatch_type"]) != `"message.created"` {
		t.Fatalf("domain/type = %s %s", got["dispatch_domain"], got["dispatch_type"])
	}
	var outbound struct {
		Mode    string `json:"mode"`
		ReplyTo string `json:"replyTo"`
	}
	if err := json.Unmarshal(got["dispatch_outbound"], &outbound); err != nil {
		t.Fatal(err)
	}
	if outbound.Mode != "dws" || outbound.ReplyTo != "latest_message" {
		t.Fatalf("outbound=%+v", outbound)
	}
}

func TestIndependentIssueTaskContextRejectsMissingOrInvalidContext(t *testing.T) {
	t.Parallel()
	for _, raw := range [][]byte{nil, []byte(`not-json`)} {
		if _, err := IndependentIssueTaskContext(raw, CoordinatorIssueTriggerCreate); err == nil {
			t.Fatalf("invalid context %q was accepted", raw)
		}
	}
	if _, err := IndependentIssueTaskContext([]byte(`{}`), CoordinatorIssueTrigger("unknown")); err == nil {
		t.Fatal("invalid coordinator Issue trigger was accepted")
	}
}
