package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeedirectory"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// directoryFake is a scripted employeedirectory.Directory.
type directoryFake struct {
	mu      sync.Mutex
	calls   map[string]int
	members []employeedirectory.GroupMember
	staff   map[string]string
	people  map[string]employeedirectory.Person
}

func (f *directoryFake) inc(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
}

func (f *directoryFake) n(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *directoryFake) Self(_ context.Context, id dwsclient.Identity) (employeedirectory.SelfEntry, error) {
	f.inc("self")
	return employeedirectory.SelfEntry{UserID: id.UID,
		Supervisor: employeedirectory.Observed{State: employeedirectory.ObservedUnregistered},
		Department: employeedirectory.Observed{State: employeedirectory.ObservedKnown, Value: "Real"},
		Title:      employeedirectory.Observed{State: employeedirectory.ObservedUnregistered}}, nil
}

func (f *directoryFake) GroupMembers(context.Context, dwsclient.Identity, string) ([]employeedirectory.GroupMember, error) {
	f.inc("members")
	return f.members, nil
}

func (f *directoryFake) ProveStaff(_ context.Context, _ dwsclient.Identity, openID string, _ []string) (string, error) {
	f.inc("prove")
	return f.staff[openID], nil
}

func (f *directoryFake) Users(_ context.Context, _ dwsclient.Identity, ids []string) ([]employeedirectory.Person, error) {
	f.inc("users")
	var out []employeedirectory.Person
	for _, id := range ids {
		if p, ok := f.people[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func installDirectoryFake(t *testing.T, fake *directoryFake) {
	t.Helper()
	previous := testHandler.EmployeeDirectory
	testHandler.EmployeeDirectory = fake
	t.Cleanup(func() {
		employeeDirectoryLazy.wg.Wait()
		testHandler.EmployeeDirectory = previous
	})
}

// seedDirectoryAgent creates a workspace with one agent and its group and
// DM scenes in org 44675729.
func seedDirectoryAgent(t *testing.T) (workspaceID, agentID string, group, dm db.AgentScene) {
	t.Helper()
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `INSERT INTO workspace(name,slug) VALUES('Employee directory test',$1) RETURNING id::text`, "employee-directory-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"employee_agent_profile_fact", "employee_scene_member_roster", "agent_scene", "agent", "agent_runtime"} {
			if _, err := testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE workspace_id=$1`, workspaceID); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
		if _, err := testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id=$1`, workspaceID); err != nil {
			t.Error(err)
		}
	})
	var runtimeID string
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_runtime(workspace_id,name,runtime_mode,provider,status,device_info,metadata,owner_id,last_seen_at)
 VALUES($1,'Directory test','cloud','employee-directory-test','online','test','{}'::jsonb,$2,now()) RETURNING id::text`, workspaceID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent(workspace_id,name,description,runtime_mode,runtime_config,runtime_id,visibility,max_concurrent_tasks,owner_id)
 VALUES($1,'Directory test','','cloud','{}'::jsonb,$2,'workspace',1,$3) RETURNING id::text`, workspaceID, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	owner := scene.Owner{WorkspaceID: parseUUID(workspaceID), AgentID: parseUUID(agentID)}
	var err error
	if group, err = scene.Resolve(ctx, testHandler.Queries, owner, scene.DingTalkConversation("44675729", scene.KindGroup, "cid-group-"+uuid.NewString()), scene.Observation{KindStated: true}); err != nil {
		t.Fatal(err)
	}
	if dm, err = scene.Resolve(ctx, testHandler.Queries, owner, scene.DingTalkConversation("44675729", scene.KindDM, "cid-dm-"+uuid.NewString()), scene.Observation{KindStated: true}); err != nil {
		t.Fatal(err)
	}
	return workspaceID, agentID, group, dm
}

func groupFake() *directoryFake {
	return &directoryFake{
		members: []employeedirectory.GroupMember{
			{OpenDingTalkID: "odt-li", Name: "李四", Role: employeedirectory.RoleOwner},
			{OpenDingTalkID: "odt-guest", Name: "Grace", Role: employeedirectory.RoleMember},
			{OpenDingTalkID: "odt-self", Name: "Qwen-Real", Role: employeedirectory.RoleMember},
		},
		staff:  map[string]string{"odt-li": "s-li", "odt-self": "507523443"},
		people: map[string]employeedirectory.Person{"s-li": {StaffID: "s-li", Title: "后端工程师", Department: "平台组"}},
	}
}

// The wake reads stored facts only; a group scene gets the roster, a DM
// never does; facts of another identity or tenant are never used; missing
// facts are refreshed in the background, once.
func TestEmployeeDirectoryFactsGroupOnlyAndIdentityBound(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	fake := groupFake()
	installDirectoryFake(t, fake)
	workspaceID, agentID, group, dm := seedDirectoryAgent(t)
	ctx := context.Background()
	req := employeeDirectoryRequest{WorkspaceID: workspaceID, AgentID: agentID, TenantOrgID: "44675729", Scene: group, DWSUID: "507523443"}

	// Nothing stored yet: unread, no roster, and one background refresh of
	// each even when several wakes ask.
	first := testHandler.employeeDirectoryFacts(ctx, req)
	second := testHandler.employeeDirectoryFacts(ctx, req)
	if first.SelfFacts != employeedirectory.SelfFactsUnread || first.GroupMembers != "" || first.RosterStatus != "none" || second.RosterStatus != "none" {
		t.Fatalf("cold context = %+v", first)
	}
	employeeDirectoryLazy.wg.Wait()
	if fake.n("self") != 1 || fake.n("members") != 1 {
		t.Fatalf("background refreshes self=%d members=%d", fake.n("self"), fake.n("members"))
	}

	warm := testHandler.employeeDirectoryFacts(ctx, req)
	if !strings.Contains(warm.SelfFacts, "直属主管 通讯录未登记") || !strings.Contains(warm.SelfFacts, "部门 Real") || warm.ProfileStatus != "loaded" {
		t.Fatalf("self facts = %+v", warm)
	}
	if warm.RosterStatus != "loaded" || !strings.Contains(warm.GroupMembers, "- 李四 · 后端工程师 · 平台组 [群主]") ||
		!strings.Contains(warm.GroupMembers, "- Grace（非本组织通讯录成员，仅显示名）") || strings.Contains(warm.GroupMembers, "Qwen-Real") {
		t.Fatalf("group members = %+v", warm)
	}
	employeeDirectoryLazy.wg.Wait()
	if fake.n("self") != 1 || fake.n("members") != 1 {
		t.Fatal("fresh facts were refreshed again")
	}

	// A DM scene has no roster, whoever the counterpart is.
	dmReq := req
	dmReq.Scene = dm
	dmReq.Prioritize = []string{"dingtalk:44675729:open_id:odt-li"}
	if c := testHandler.employeeDirectoryFacts(ctx, dmReq); c.GroupMembers != "" || c.RosterStatus != "not_group" || c.ProfileStatus != "loaded" {
		t.Fatalf("dm context = %+v", c)
	}
	// Another tenant than the scene's: no roster.
	otherTenant := req
	otherTenant.TenantOrgID = "99999"
	if c := testHandler.employeeDirectoryFacts(ctx, otherTenant); c.GroupMembers != "" || c.RosterStatus != "unavailable" || c.ProfileStatus != "unread" {
		t.Fatalf("other tenant context = %+v", c)
	}
	// Another execution identity: nothing read as the old one is used.
	rebound := req
	rebound.DWSUID = "600000001"
	if c := testHandler.employeeDirectoryFacts(ctx, rebound); c.SelfFacts != employeedirectory.SelfFactsUnread || c.GroupMembers != "" {
		t.Fatalf("rebound context = %+v", c)
	}
	employeeDirectoryLazy.wg.Wait()
	// One profile read for the other tenant's wake, one for the rebound
	// identity, and one roster read as the rebound identity.
	if fake.n("self") != 3 || fake.n("members") != 2 {
		t.Fatalf("rebound refreshes self=%d members=%d", fake.n("self"), fake.n("members"))
	}
	if c := testHandler.employeeDirectoryFacts(ctx, rebound); c.ProfileStatus != "loaded" || c.RosterStatus != "loaded" {
		t.Fatalf("rebound after refresh = %+v", c)
	}
}

func TestDeleteWorkspace_CleansEmployeeDirectoryFacts(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	target, neighbor := seedWorkspaceEmployeeTask(t), seedWorkspaceEmployeeTask(t)
	database, _ := employeeEntryDB(testHandler)
	store := employeedirectory.NewStore(database)
	for _, ws := range []string{target, neighbor} {
		var agentID, sceneID, conversation string
		if err := testPool.QueryRow(ctx, `SELECT agent_id::text, id::text, external_scene_id FROM agent_scene WHERE workspace_id=$1 LIMIT 1`, ws).Scan(&agentID, &sceneID, &conversation); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for _, table := range []string{"employee_agent_profile_fact", "employee_scene_member_roster"} {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE workspace_id=$1`, ws)
			}
		})
		r := &employeedirectory.Refresher{Store: store, Directory: groupFake()}
		if rep, err := r.RefreshProfile(ctx, employeedirectory.ProfileTarget{WorkspaceID: ws, AgentID: agentID, TenantOrgID: "employee-delete-org", DWSUID: "507523443"}); err != nil || rep.Outcome != employeedirectory.OutcomeRefreshed {
			t.Fatalf("profile: %+v %v", rep, err)
		}
		if rep, err := r.RefreshRoster(ctx, employeedirectory.RosterTarget{WorkspaceID: ws, AgentID: agentID, SceneID: sceneID, TenantOrgID: "employee-delete-org", DWSUID: "507523443", ConversationID: conversation}); err != nil || rep.Outcome != employeedirectory.OutcomeRefreshed {
			t.Fatalf("roster: %+v %v", rep, err)
		}
	}
	count := func(ws string) (int, int) {
		var facts, rosters int
		if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_agent_profile_fact WHERE workspace_id=$1), (SELECT count(*) FROM employee_scene_member_roster WHERE workspace_id=$1)`, ws).Scan(&facts, &rosters); err != nil {
			t.Fatal(err)
		}
		return facts, rosters
	}
	if f, r := count(target); f != 3 || r != 1 {
		t.Fatalf("seeded target facts=%d rosters=%d", f, r)
	}
	response := httptest.NewRecorder()
	testHandler.DeleteWorkspace(response, withURLParam(newRequest(http.MethodDelete, "/api/workspaces/"+target, nil), "id", target))
	if response.Code != http.StatusNoContent {
		t.Fatalf("DeleteWorkspace: %d %s", response.Code, response.Body.String())
	}
	if f, r := count(target); f != 0 || r != 0 {
		t.Fatalf("target after delete facts=%d rosters=%d", f, r)
	}
	if f, r := count(neighbor); f != 3 || r != 1 {
		t.Fatalf("neighbor after delete facts=%d rosters=%d", f, r)
	}
}
