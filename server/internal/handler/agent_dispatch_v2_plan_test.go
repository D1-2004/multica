package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestBuildAgentDispatchExecutionPlanComposesSurfaceIdentityAndOutbound(t *testing.T) {
	principal := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	namespace := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	workspace := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	agent := pgtype.UUID{Bytes: [16]byte{4}, Valid: true}
	dispatchContext := agentDispatchContext{
		EndpointNamespaceID: namespace,
		UserID:              principal,
		WorkspaceID:         workspace,
		AgentID:             agent,
	}

	for _, tc := range []struct {
		name                   string
		sourceType             string
		surfaceType            string
		outboundMode           string
		wantWorkflow           bool
		wantServerOutboundMute bool
		wantEndpointNamespace  bool
	}{
		{name: "digital employee issue through DWS", sourceType: "digital_employee", surfaceType: "issue", outboundMode: "dws", wantWorkflow: true, wantServerOutboundMute: true},
		{name: "digital employee chat through robot SDK", sourceType: "digital_employee", surfaceType: "chat", outboundMode: "robot_sdk"},
		{name: "digital employee chat through DWS", sourceType: "digital_employee", surfaceType: "chat", outboundMode: "dws", wantWorkflow: true, wantServerOutboundMute: true, wantEndpointNamespace: true},
		{name: "robot issue through DWS", sourceType: "robot", surfaceType: "issue", outboundMode: "dws", wantWorkflow: true, wantServerOutboundMute: true},
		{name: "robot chat through DWS", sourceType: "robot", surfaceType: "chat", outboundMode: "dws", wantWorkflow: true, wantServerOutboundMute: true},
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
			if (plan.InstallationOverride != nil) != tc.wantEndpointNamespace {
				t.Fatalf("installation override present = %t, want %t", plan.InstallationOverride != nil, tc.wantEndpointNamespace)
			}
			if plan.InstallationOverride != nil {
				if plan.InstallationOverride.ID != namespace ||
					plan.InstallationOverride.WorkspaceID != workspace ||
					plan.InstallationOverride.AgentID != agent ||
					plan.InstallationOverride.InstallerUserID != principal ||
					!plan.InstallationOverride.Active {
					t.Fatalf("installation override = %+v, want authenticated endpoint scope", plan.InstallationOverride)
				}
			}
		})
	}
}

func TestBuildAgentDispatchExecutionPlanRejectsDigitalEmployeeChatWithoutEndpointNamespace(t *testing.T) {
	command := dispatchCommandForPlanTest("digital_employee", "chat", "dws")
	_, err := buildAgentDispatchExecutionPlan(command, agentDispatchContext{
		UserID:      pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		WorkspaceID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		AgentID:     pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
	})
	if err == nil {
		t.Fatal("digital employee chat+DWS accepted without authenticated endpoint namespace")
	}
}

func TestBuildAgentDispatchExecutionPlanRejectsDigitalEmployeeChatWithoutEndpointPrincipal(t *testing.T) {
	command := dispatchCommandForPlanTest("digital_employee", "chat", "dws")
	_, err := buildAgentDispatchExecutionPlan(command, agentDispatchContext{
		EndpointNamespaceID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		WorkspaceID:         pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		AgentID:             pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
	})
	if err == nil {
		t.Fatal("digital employee chat+DWS accepted without authenticated endpoint principal")
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
