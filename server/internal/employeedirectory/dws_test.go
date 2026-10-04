package employeedirectory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/pkg/dws"
)

// fakeGateway answers DWS tool calls with scripted payloads, shaped like
// the production gateway's (probed 2026-10-03, contact values redacted).
type fakeGateway struct {
	mu    sync.Mutex
	calls []string
	args  []map[string]any
	reply func(tool string, args map[string]any) string
}

func (g *fakeGateway) directory(t *testing.T) DWSDirectory {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		g.mu.Lock()
		g.calls = append(g.calls, req.Params.Name)
		g.args = append(g.args, req.Params.Arguments)
		g.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"content": []map[string]string{{"type": "text", "text": g.reply(req.Params.Name, req.Params.Arguments)}}}})
	}))
	t.Cleanup(srv.Close)
	client, err := dws.NewWithToken(context.Background(), dws.Config{GatewayURL: srv.URL, SkipVerify: true}, dws.Token{AccessToken: "token-under-test"})
	if err != nil {
		t.Fatal(err)
	}
	return DWSDirectory{Client: func(context.Context, dwsclient.Identity) (*dws.Client, error) { return client, nil }}
}

var qwenReal = dwsclient.Identity{AgentID: "33af235e-e03b-4be2-be3b-bbae8b97fce5", UID: "507523443", OrgID: "44675729"}

// The probed shape of Qwen-Real's own entry: no supervisor, no title, one
// department; contact fields present and never kept.
const qwenRealEntry = `{"success":true,"result":[{"isAdmin":false,"orgEmployeeModel":{"depts":[{"deptId":1043992632,"deptName":"Real","deptPathName":"Real"}],
	"jobNumber":"J-0001","labels":[],"orgAuthEmail":"real@corp.example.com","orgId":null,"orgMasterDisplayName":null,"orgMasterUserId":null,
	"orgName":"Real Niubility","orgTitle":null,"orgUserId":"507523443","orgUserName":"Qwen-Real","orgUserMobile":"13800138000","positions":[]}}]}`

func TestDWSSelfUnregisteredSupervisorAndTitle(t *testing.T) {
	g := &fakeGateway{reply: func(tool string, args map[string]any) string { return qwenRealEntry }}
	entry, err := g.directory(t).Self(context.Background(), qwenReal)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Supervisor.State != ObservedUnregistered || entry.Title.State != ObservedUnregistered ||
		entry.Department.State != ObservedKnown || entry.Department.Value != "Real" {
		t.Fatalf("entry = %+v", entry)
	}
	if len(g.calls) != 1 || g.calls[0] != "get_user_info_by_user_ids" {
		t.Fatalf("calls = %v", g.calls)
	}
	ids, _ := g.args[0]["user_id_list"].([]any)
	if len(ids) != 1 || ids[0] != "507523443" {
		t.Fatalf("args = %v", g.args[0])
	}
}

func TestDWSSelfSupervisorNamedByIDAndMainPosition(t *testing.T) {
	g := &fakeGateway{reply: func(tool string, args map[string]any) string {
		ids, _ := args["user_id_list"].([]any)
		if len(ids) == 1 && ids[0] == "024083" {
			return `{"success":true,"result":[{"orgEmployeeModel":{"orgUserId":"024083","orgUserName":"朱鸿","orgUserMobile":"13900139000"}}]}`
		}
		return `{"success":true,"result":[{"orgEmployeeModel":{"orgUserId":"507523443","orgUserName":"Qwen-Real","orgMasterUserId":"024083","orgMasterDisplayName":null,
			"orgTitle":"","depts":[{"deptId":1,"deptName":"总部"},{"deptId":996530034,"deptName":"AI终端与创新"}],
			"positions":[{"deptId":996530034,"isMain":true,"title":"数字员工"}]}}]}`
	}}
	entry, err := g.directory(t).Self(context.Background(), qwenReal)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Supervisor.Value != "朱鸿" || entry.Supervisor.Ref != "dingtalk:44675729:staff_id:024083" ||
		entry.Department.Value != "AI终端与创新" || entry.Title.Value != "数字员工" {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestDWSSelfFallsBackToCurrentProfileAndChecksIdentity(t *testing.T) {
	self := `{"success":true,"result":[{"orgEmployeeModel":{"userId":"507523443","orgUserName":"Qwen-Real","orgMasterUserId":"024083","orgMasterDisplayName":"朱鸿",
		"depts":[{"deptId":1043992632,"deptName":"Real"}],"orgEmail":"x@corp.example.com"}}]}`
	g := &fakeGateway{reply: func(tool string, args map[string]any) string {
		if tool == "get_user_info_by_user_ids" {
			return `{"success":true,"result":[]}`
		}
		return self
	}}
	dir := g.directory(t)
	entry, err := dir.Self(context.Background(), qwenReal)
	if err != nil {
		t.Fatal(err)
	}
	// get_current_user_profile has no title: unavailable, never "unregistered".
	if entry.Title.State != ObservedUnavailable || entry.Supervisor.Value != "朱鸿" || entry.Department.Value != "Real" {
		t.Fatalf("entry = %+v", entry)
	}
	self = strings.Replace(self, `"userId":"507523443"`, `"userId":"999"`, 1)
	if _, err := dir.Self(context.Background(), qwenReal); !errors.Is(err, ErrIdentityMismatch) || classify(err) != "identity_mismatch" {
		t.Fatalf("mismatch: %v", err)
	}
}

func TestDWSGroupMembersAndUsers(t *testing.T) {
	g := &fakeGateway{reply: func(tool string, args map[string]any) string {
		switch tool {
		case "get_group_members":
			return `{"success":true,"result":{"hasMore":false,"list":[
				{"openDingtalkId":"odt-1","memberEmpName":"张三","memberNick":"zs","memberGroupNick":"","memberRoleDesc":"群主","memberRoleType":1,"memberDingtalkId":"$:LWCP_v1:$x"},
				{"openDingtalkId":"odt-2","memberEmpName":"","memberNick":"Grace","memberGroupNick":"G","memberRoleDesc":"管理员","memberRoleType":2},
				{"openDingtalkId":"odt-3","memberEmpName":"李四","memberNick":"ls","memberRoleDesc":"普通成员","memberRoleType":3}]}}`
		case "get_user_info_by_user_ids":
			return `{"success":true,"result":[{"orgEmployeeModel":{"orgUserId":"s-1","orgUserName":"张三","orgTitle":"产品经理 13800138000",
				"depts":[{"deptId":5,"deptName":"产品部"}],"orgEmail":"zs@corp.example.com","orgUserMobile":"13800138000"}}]}`
		}
		return `{"success":true,"result":[]}`
	}}
	dir := g.directory(t)
	members, err := dir.GroupMembers(context.Background(), qwenReal, "cid-1")
	if err != nil || len(members) != 3 || members[0].Role != RoleOwner || members[1].Role != RoleAdmin || members[2].Role != RoleMember ||
		members[1].Name != "Grace" || members[2].Name != "李四" {
		t.Fatalf("members = %+v %v", members, err)
	}
	people, err := dir.Users(context.Background(), qwenReal, []string{"s-1"})
	if err != nil || len(people) != 1 || people[0].Title != "产品经理" || people[0].Department != "产品部" {
		t.Fatalf("people = %+v %v", people, err)
	}
	raw, _ := json.Marshal(people)
	if strings.Contains(string(raw), "1380013") || strings.Contains(string(raw), "@") {
		t.Fatalf("contact detail kept: %s", raw)
	}
}

func TestDWSFailuresAreClassified(t *testing.T) {
	g := &fakeGateway{reply: func(string, map[string]any) string {
		return `{"success":false,"errorCode":"FORBIDDEN","errorMsg":"no permission","result":null}`
	}}
	_, err := g.directory(t).GroupMembers(context.Background(), qwenReal, "cid-1")
	if err == nil || classify(err) != "forbidden" {
		t.Fatalf("err = %v (%s)", err, classify(err))
	}
	if strings.Contains(err.Error(), "token-under-test") {
		t.Fatal("token leaked into the error")
	}
	if classify(context.DeadlineExceeded) != "timeout" || errorCode("weird") != "provider_error" {
		t.Fatal("classification")
	}
}
