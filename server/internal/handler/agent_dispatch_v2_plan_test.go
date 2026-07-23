package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestBuildAgentDispatchExecutionPlanComposesSurfaceIdentityAndOutbound(t *testing.T) {
	principal := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	dispatchContext := agentDispatchContext{UserID: principal}

	for _, tc := range []struct {
		name                   string
		sourceType             string
		surfaceType            string
		outboundMode           string
		wantWorkflow           bool
		wantServerOutboundMute bool
	}{
		{name: "digital employee issue through DWS", sourceType: "digital_employee", surfaceType: "issue", outboundMode: "dws", wantWorkflow: true, wantServerOutboundMute: true},
		{name: "digital employee chat through robot SDK", sourceType: "digital_employee", surfaceType: "chat", outboundMode: "robot_sdk"},
		{name: "robot issue through DWS", sourceType: "robot", surfaceType: "issue", outboundMode: "dws", wantWorkflow: true, wantServerOutboundMute: true},
		{name: "robot chat through robot SDK", sourceType: "robot", surfaceType: "chat", outboundMode: "robot_sdk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := dispatchCommandForPlanTest(tc.sourceType, tc.surfaceType, tc.outboundMode)
			plan, err := buildAgentDispatchExecutionPlan(command, dispatchContext)
			if err != nil {
				t.Fatal(err)
			}
			if plan.SurfaceType != tc.surfaceType {
				t.Fatalf("surface = %q, want %q", plan.SurfaceType, tc.surfaceType)
			}
			if plan.Identity.PrincipalUserID != principal || plan.Identity.InitiatorUserID.Valid {
				t.Fatalf("identity = %+v, want authenticated endpoint principal", plan.Identity)
			}
			if (plan.Prompt.WorkflowPrompt != "") != tc.wantWorkflow {
				t.Fatalf("workflow prompt present = %t, want %t", plan.Prompt.WorkflowPrompt != "", tc.wantWorkflow)
			}
			if plan.SuppressServerOutbound != tc.wantServerOutboundMute {
				t.Fatalf("suppress server outbound = %t, want %t", plan.SuppressServerOutbound, tc.wantServerOutboundMute)
			}
			if !plan.DisableControlCommands {
				t.Fatal("dispatch surface can be overridden by channel slash commands")
			}
		})
	}
}

func dispatchCommandForPlanTest(sourceType, surfaceType, outboundMode string) DispatchCommand {
	return DispatchCommand{
		SchemaVersion: "2.0",
		AgentID:       "agent-id",
		Source:        DispatchSource{Platform: "dingtalk", Type: sourceType},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-1"},
			Sender:       DispatchSender{OpenDingTalkID: "sender-1"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-1", Text: "处理一下"}},
		}},
		Surface:  DispatchSurface{Type: surfaceType},
		Outbound: DispatchOutbound{Mode: outboundMode, ReplyTo: "latest_message"},
	}
}
