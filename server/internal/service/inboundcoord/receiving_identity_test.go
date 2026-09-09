package inboundcoord

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type receivingQueries struct {
	coordQueriesStub
	binding db.ChannelInstallation
}

func (q *receivingQueries) GetDingTalkAccountBindingByAgent(context.Context, db.GetDingTalkAccountBindingByAgentParams) (db.ChannelInstallation, error) {
	return q.binding, nil
}

func TestReceivingIdentityUsesMessageBindingWithoutExecutionAuthorization(t *testing.T) {
	aid, _ := util.ParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	wid, _ := util.ParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	turn := Turn{Source: SourceDigitalEmployee, AgentID: aid, WorkspaceID: util.UUIDToString(wid), DWSUID: "receiving", DWSOrgID: "org", AgentName: "配置标题"}
	binding := db.ChannelInstallation{AgentID: aid, WorkspaceID: wid, Status: "active", ChannelType: "dingtalk_account", Config: []byte(`{"account_display_name":"群聊接收账号","router_account_id":"receiving","router_tenant_id":"org"}`)}
	q := &receivingQueries{coordQueriesStub: coordQueriesStub{accountDisplayName: "另一执行账号"}, binding: binding}
	(&Coordinator{Queries: q}).FillVoice(context.Background(), &turn)
	if turn.EmployeeAccountName != "群聊接收账号" {
		t.Fatalf("message identity lost: %q", turn.EmployeeAccountName)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*db.ChannelInstallation)
		want   string
	}{
		{"legacy message only", func(b *db.ChannelInstallation) { b.Config = []byte(`{"account_display_name":"群聊接收账号"}`) }, "群聊接收账号"},
		{"different account", func(b *db.ChannelInstallation) {
			b.Config = []byte(`{"account_display_name":"错误账号","router_account_id":"other","router_tenant_id":"org"}`)
		}, ""},
		{"different org", func(b *db.ChannelInstallation) {
			b.Config = []byte(`{"account_display_name":"错误账号","router_account_id":"receiving","router_tenant_id":"other"}`)
		}, ""},
		{"partial key", func(b *db.ChannelInstallation) {
			b.Config = []byte(`{"account_display_name":"错误账号","router_account_id":"receiving"}`)
		}, ""},
		{"revoked", func(b *db.ChannelInstallation) { b.Status = "revoked" }, ""},
		{"wrong agent", func(b *db.ChannelInstallation) { b.AgentID = wid }, ""},
		{"wrong channel", func(b *db.ChannelInstallation) { b.ChannelType = "other" }, ""},
		{"malformed", func(b *db.ChannelInstallation) { b.Config = []byte(`{`) }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q.binding = binding
			tc.mutate(&q.binding)
			(&Coordinator{Queries: q}).FillVoice(context.Background(), &turn)
			if turn.EmployeeAccountName != tc.want {
				t.Fatalf("got %q want %q", turn.EmployeeAccountName, tc.want)
			}
		})
	}
}
