package employeedirectory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func refreshRoster(t *testing.T, r *Refresher, target RosterTarget) Roster {
	t.Helper()
	rep, err := r.RefreshRoster(context.Background(), target)
	if err != nil || rep.Outcome != OutcomeRefreshed {
		t.Fatalf("refresh: %+v %v", rep, err)
	}
	roster, ok, err := r.Store.ReadRoster(context.Background(), target.WorkspaceID, target.AgentID, target.SceneID)
	if err != nil || !ok {
		t.Fatalf("read roster: %v %v", ok, err)
	}
	return roster
}

func memberByName(r Roster, name string) []Member {
	var out []Member
	for _, m := range r.Members {
		if m.Name == name {
			out = append(out, m)
		}
	}
	return out
}

// Members the agent's own org address book proves get title and department;
// everyone else, including members of another org, keeps a name only, even
// if the directory would describe them.
func TestRosterCrossOrgNameOnly(t *testing.T) {
	a, _ := testSchema(t)
	target := newRosterTarget()
	dir := &fakeDirectory{
		members: []GroupMember{
			{OpenDingTalkID: "odt-owner", Name: "张三", Role: RoleOwner},
			{OpenDingTalkID: "odt-lisi", Name: "李四", Role: RoleMember},
			{OpenDingTalkID: "odt-guest", Name: "Grace（外部）", Role: RoleMember},
			{OpenDingTalkID: "odt-self", Name: "Qwen-Real", Role: RoleMember},
		},
		staff: map[string]string{"odt-owner": "s-zhang", "odt-lisi": "s-li", "odt-self": "507523443"},
		people: map[string]Person{
			"s-zhang": {StaffID: "s-zhang", Name: "张三", Title: "产品经理", Department: "产品部"},
			"s-li":    {StaffID: "s-li", Name: "李四", Title: "后端工程师", Department: "平台组"},
			// Never asked for: the guest is not proved same-org.
			"s-guest": {StaffID: "s-guest", Name: "Grace", Title: "CEO", Department: "Guest Corp"},
		},
	}
	r := &Refresher{Store: NewStore(a), Directory: dir}
	roster := refreshRoster(t, r, target)
	if roster.Total != 4 || len(roster.Members) != 4 {
		t.Fatalf("roster = %+v", roster)
	}
	li := memberByName(roster, "李四")
	if len(li) != 1 || li[0].Org != OrgSame || li[0].Title != "后端工程师" || li[0].Department != "平台组" ||
		li[0].Ref != "dingtalk:44675729:open_id:odt-lisi" || li[0].StaffRef != "dingtalk:44675729:staff_id:s-li" {
		t.Fatalf("李四 = %+v", li)
	}
	guest := memberByName(roster, "Grace（外部）")
	if len(guest) != 1 || guest[0].Org != OrgOther || guest[0].Title != "" || guest[0].Department != "" || guest[0].StaffRef != "" {
		t.Fatalf("guest = %+v", guest)
	}
	self := memberByName(roster, "Qwen-Real")
	if len(self) != 1 || !self[0].Self {
		t.Fatalf("self = %+v", self)
	}
	block := RenderGroupMembers(roster, RenderOptions{})
	for _, want := range []string{"GROUP MEMBERS", "- 张三 · 产品经理 · 产品部 [群主]", "- 李四 · 后端工程师 · 平台组", "- Grace（外部）（非本组织通讯录成员，仅显示名）"} {
		if !strings.Contains(block, want) {
			t.Fatalf("block misses %q:\n%s", want, block)
		}
	}
	for _, never := range []string{"CEO", "Guest Corp", "Qwen-Real", "odt-", "s-li", "staff_id"} {
		if strings.Contains(block, never) {
			t.Fatalf("block leaks %q:\n%s", never, block)
		}
	}
	if dir.n("users") != 1 {
		t.Fatalf("users calls = %d", dir.n("users"))
	}
}

// Two members named 张伟 are both listed and flagged; title and department
// tell them apart. Two cross-org 李娜 cannot be told apart, and the block says so.
func TestRosterAmbiguityFlagged(t *testing.T) {
	a, _ := testSchema(t)
	target := newRosterTarget()
	members := []GroupMember{{OpenDingTalkID: "odt-w1", Name: "张伟"}, {OpenDingTalkID: "odt-x", Name: "王五"}}
	for i := range 40 {
		members = append(members, GroupMember{OpenDingTalkID: fmt.Sprintf("odt-f%02d", i), Name: fmt.Sprintf("成员%02d", i)})
	}
	members = append(members, GroupMember{OpenDingTalkID: "odt-w2", Name: " 张伟 "},
		GroupMember{OpenDingTalkID: "odt-n1", Name: "李娜"}, GroupMember{OpenDingTalkID: "odt-n2", Name: "李娜"})
	dir := &fakeDirectory{members: members,
		staff:  map[string]string{"odt-w1": "s-w1", "odt-w2": "s-w2"},
		people: map[string]Person{"s-w1": {StaffID: "s-w1", Title: "测试工程师", Department: "质量部"}, "s-w2": {StaffID: "s-w2", Title: "销售", Department: "华东大区"}}}
	r := &Refresher{Store: NewStore(a), Directory: dir, SearchBudget: 100}
	roster := refreshRoster(t, r, target)
	block := RenderGroupMembers(roster, RenderOptions{MaxMembers: 30})
	if strings.Count(block, "张伟") != 2 || strings.Count(block, "【同名 2 人，按职位/部门区分】") != 2 {
		t.Fatalf("张伟 not both flagged:\n%s", block)
	}
	if !strings.Contains(block, "张伟 · 测试工程师 · 质量部") || !strings.Contains(block, "张伟 · 销售 · 华东大区") {
		t.Fatalf("张伟 lines lack disambiguation:\n%s", block)
	}
	// Prioritizing one 李娜 lists both, flagged as indistinguishable.
	block = RenderGroupMembers(roster, RenderOptions{MaxMembers: 30, Prioritize: []string{"dingtalk:44675729:open_id:odt-n2"}})
	if strings.Count(block, "李娜") != 2 || strings.Count(block, "无法区分") != 2 {
		t.Fatalf("李娜 ambiguity:\n%s", block)
	}
	lines := strings.Split(block, "\n")
	if !strings.HasPrefix(lines[1], "- 李娜") || !strings.HasPrefix(lines[2], "- 李娜") {
		t.Fatalf("prioritized members not first:\n%s", block)
	}
}

// The block lists at most 30 members within its byte budget and counts the
// rest; storage and directory calls are bounded too.
func TestRosterBoundedRendering(t *testing.T) {
	a, _ := testSchema(t)
	target := newRosterTarget()
	var members []GroupMember
	staff := map[string]string{}
	people := map[string]Person{}
	for i := range 300 {
		odt := fmt.Sprintf("odt-%03d", i)
		members = append(members, GroupMember{OpenDingTalkID: odt, Name: fmt.Sprintf("同事%03d", i)})
		staff[odt] = fmt.Sprintf("s-%03d", i)
		people[staff[odt]] = Person{StaffID: staff[odt], Title: strings.Repeat("高级", 15), Department: strings.Repeat("部门", 20)}
	}
	dir := &fakeDirectory{members: members, staff: staff, people: people}
	r := &Refresher{Store: NewStore(a), Directory: dir}
	roster := refreshRoster(t, r, target)
	if roster.Total != 300 || len(roster.Members) != 200 {
		t.Fatalf("total %d stored %d", roster.Total, len(roster.Members))
	}
	if dir.n("prove") != 30 {
		t.Fatalf("searches = %d, want the budget 30", dir.n("prove"))
	}
	block := RenderGroupMembers(roster, RenderOptions{})
	lines := strings.Split(block, "\n")
	listed := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "- ") {
			listed++
		}
	}
	if listed == 0 || listed > 30 || len(block) > 4096 {
		t.Fatalf("listed %d, %d bytes", listed, len(block))
	}
	if want := fmt.Sprintf("另有 %d 位成员未列出（本群共 300 人，含本账号）。", 300-listed); !strings.Contains(block, want) {
		t.Fatalf("missing %q:\n%s", want, block)
	}
	small := RenderGroupMembers(Roster{RefreshedAt: roster.RefreshedAt, Total: 45, Members: roster.Members[:45]}, RenderOptions{MaxBytes: 1 << 20})
	if strings.Count(small, "\n- ") != 30 || !strings.Contains(small, "另有 15 位成员未列出") {
		t.Fatalf("small block:\n%s", small)
	}
}

// Phone numbers and email addresses never reach the roster or the block,
// even when the directory puts them into a display field.
func TestRosterPrivacySentinels(t *testing.T) {
	a, _ := testSchema(t)
	target := newRosterTarget()
	dir := &fakeDirectory{
		members: []GroupMember{{OpenDingTalkID: "odt-1", Name: "赵六 13800138000"}, {OpenDingTalkID: "odt-2", Name: "钱七", GroupNick: "qq@example.com"}},
		staff:   map[string]string{"odt-1": "s-1", "odt-2": "s-2"},
		people: map[string]Person{
			"s-1": {StaffID: "s-1", Title: "电话 138-0013-8000", Department: "zhao@corp.example.com"},
			"s-2": {StaffID: "s-2", Title: "经理", Department: "市场部"},
		},
	}
	// The DWS layer cleans what it decodes; simulate a directory that does not.
	r := &Refresher{Store: NewStore(a), Directory: dir}
	roster := refreshRoster(t, r, target)
	raw, _ := json.Marshal(roster.Members)
	block := RenderGroupMembers(roster, RenderOptions{})
	for _, s := range []string{string(raw), block} {
		for _, never := range []string{"13800138000", "138-0013-8000", "@example.com", "@corp.example.com"} {
			if strings.Contains(s, never) {
				t.Fatalf("leak %q in %s", never, s)
			}
		}
	}
	if !strings.Contains(block, "- 钱七 · 经理 · 市场部") || !strings.Contains(block, "- 赵六 · 电话\n") {
		t.Fatalf("block:\n%s", block)
	}
}

// A failed member list keeps the previous roster; a failed check keeps a
// member's previous classification and facts.
func TestRosterErrorKeepsOldMembers(t *testing.T) {
	a, _ := testSchema(t)
	ctx := context.Background()
	target := newRosterTarget()
	dir := &fakeDirectory{
		members: []GroupMember{{OpenDingTalkID: "odt-li", Name: "李四"}, {OpenDingTalkID: "odt-g", Name: "Guest"}},
		staff:   map[string]string{"odt-li": "s-li"},
		people:  map[string]Person{"s-li": {StaffID: "s-li", Title: "后端工程师", Department: "平台组"}},
	}
	r := &Refresher{Store: NewStore(a), Directory: dir}
	first := refreshRoster(t, r, target)

	expireRoster(t, a, target)
	dir.listErr = errors.New("gateway down")
	rep, err := r.RefreshRoster(ctx, target)
	if err != nil || rep.Outcome != OutcomeFailed || rep.ErrorCode != "provider_error" {
		t.Fatalf("failed refresh: %+v %v", rep, err)
	}
	kept, ok, err := r.Store.ReadRoster(ctx, target.WorkspaceID, target.AgentID, target.SceneID)
	if err != nil || !ok || len(kept.Members) != len(first.Members) || kept.ErrorCode != "provider_error" || !kept.RefreshedAt.Before(first.RefreshedAt) {
		t.Fatalf("kept = %+v %v %v", kept, ok, err)
	}
	if block := RenderGroupMembers(kept, RenderOptions{}); !strings.Contains(block, "李四 · 后端工程师 · 平台组") || !strings.Contains(block, "最近一次刷新失败") {
		t.Fatalf("block:\n%s", block)
	}

	// Next day: the list works but the address book search and the facts
	// read fail. 李四 keeps same-org facts (a kept proof would also do), the
	// guest keeps "other".
	if _, err := a.Exec(ctx, `UPDATE employee_scene_member_roster SET error_code = '' WHERE scene_id = $1::uuid`, target.SceneID); err != nil {
		t.Fatal(err)
	}
	expireRoster(t, a, target)
	dir.listErr = nil
	dir.proveErr = map[string]error{"odt-li": errors.New("search failed"), "odt-g": errors.New("search failed")}
	dir.usersErr = errors.New("user info failed")
	next := refreshRoster(t, r, target)
	li, guest := memberByName(next, "李四"), memberByName(next, "Guest")
	if len(li) != 1 || li[0].Org != OrgSame || li[0].Title != "后端工程师" || len(guest) != 1 || guest[0].Org != OrgOther {
		t.Fatalf("next = %+v", next.Members)
	}
}

// A kept proof (dws_open_identity_staff, within 7 days) needs no search; a
// new proof is remembered for the native sender path.
func TestRosterKeptStaffSkipsSearch(t *testing.T) {
	a, _ := testSchema(t)
	ctx := context.Background()
	target := newRosterTarget()
	if _, err := a.Exec(ctx, `INSERT INTO dws_open_identity_staff (org_id, viewer_uid, open_dingtalk_id, staff_id) VALUES ($1, $2, 'odt-kept', 's-kept')`, target.TenantOrgID, target.DWSUID); err != nil {
		t.Fatal(err)
	}
	var remembered []string
	dir := &fakeDirectory{
		members: []GroupMember{{OpenDingTalkID: "odt-kept", Name: "老王"}, {OpenDingTalkID: "odt-new", Name: "小李"}},
		staff:   map[string]string{"odt-new": "s-new"},
		people:  map[string]Person{"s-kept": {StaffID: "s-kept", Title: "架构师"}, "s-new": {StaffID: "s-new", Title: "实习生"}},
	}
	r := &Refresher{Store: NewStore(a), Directory: dir, Remember: func(_ context.Context, org, viewer, odt, staff string) error {
		remembered = append(remembered, org+"/"+viewer+"/"+odt+"/"+staff)
		return nil
	}}
	roster := refreshRoster(t, r, target)
	if dir.n("prove") != 1 || len(remembered) != 1 || remembered[0] != "44675729/507523443/odt-new/s-new" {
		t.Fatalf("searches %d remembered %v", dir.n("prove"), remembered)
	}
	if m := memberByName(roster, "老王"); len(m) != 1 || m[0].Title != "架构师" || m[0].Org != OrgSame {
		t.Fatalf("kept member = %+v", m)
	}
}

// Prioritized refs that are not members of this group (a DM counterpart, a
// sender of another scene) are never rendered.
func TestRosterPrioritizeIgnoresNonMembers(t *testing.T) {
	a, _ := testSchema(t)
	target := newRosterTarget()
	dir := &fakeDirectory{members: []GroupMember{{OpenDingTalkID: "odt-a", Name: "甲"}}}
	r := &Refresher{Store: NewStore(a), Directory: dir}
	roster := refreshRoster(t, r, target)
	block := RenderGroupMembers(roster, RenderOptions{Prioritize: []string{"dingtalk:44675729:open_id:odt-dm-friend", "dingtalk:44675729:staff_id:s-dm"}})
	if strings.Contains(block, "odt-dm") || strings.Count(block, "\n- ") != 1 {
		t.Fatalf("block:\n%s", block)
	}
	if _, ok := roster.Match("dingtalk:44675729:open_id:odt-dm-friend"); ok {
		t.Fatal("a non-member matched")
	}
}

// Two replicas race for one scene: one reads DingTalk, the other does not.
func TestRosterRefreshLeaseTwoReplicas(t *testing.T) {
	a, b := testSchema(t)
	target := newRosterTarget()
	dirA := &fakeDirectory{members: []GroupMember{{OpenDingTalkID: "odt-a", Name: "甲"}}}
	dirB := &fakeDirectory{members: []GroupMember{{OpenDingTalkID: "odt-a", Name: "甲"}}}
	results := make(chan Report, 2)
	for _, r := range []*Refresher{{Store: NewStore(a), Directory: dirA}, {Store: NewStore(b), Directory: dirB}} {
		go func(r *Refresher) {
			rep, err := r.RefreshRoster(context.Background(), target)
			if err != nil {
				t.Error(err)
			}
			results <- rep
		}(r)
	}
	outcomes := map[string]int{}
	for range 2 {
		outcomes[(<-results).Outcome]++
	}
	if outcomes[OutcomeRefreshed] != 1 || outcomes[OutcomeNotDue] != 1 || dirA.n("members")+dirB.n("members") != 1 {
		t.Fatalf("outcomes %v, member reads %d+%d", outcomes, dirA.n("members"), dirB.n("members"))
	}
}

// Group scenes of employee-mode agents in the identity org are scanned when
// active and due; DMs and other tenant orgs are not.
func TestDueRosterTargetsOnlyActiveGroupsInIdentityOrg(t *testing.T) {
	a, _ := testSchema(t)
	ctx := context.Background()
	ws, agent := uuid.NewString(), uuid.NewString()
	if _, err := a.Exec(ctx, `INSERT INTO agent (id, workspace_id, coordination_mode, inbound_coordinator) VALUES ($1, $2, 'employee', true)`, agent, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, `INSERT INTO agent_dingtalk_identity (agent_id, workspace_id, dws_uid, org_id) VALUES ($1, $2, '507523443', '44675729')`, agent, ws); err != nil {
		t.Fatal(err)
	}
	scene := func(kind, org, active string) string {
		var id string
		if err := a.QueryRow(ctx, `INSERT INTO agent_scene (workspace_id, agent_id, provider, tenant_org_id, source_namespace, scene_kind, external_scene_id, last_active_at)
			VALUES ($1, $2, 'dingtalk', $3, 'dingtalk:conversation', $4, $5, now() - $6::interval) RETURNING id::text`, ws, agent, org, kind, "cid-"+uuid.NewString(), active).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	group := scene("group", "44675729", "1 hour")
	scene("dm", "44675729", "1 hour")
	scene("group", "99999", "1 hour")
	scene("group", "44675729", "10 days")
	store := NewStore(a)
	due, err := store.DueRosterTargets(ctx, DefaultSchedule, 0, 10)
	if err != nil || len(due) != 1 || due[0].SceneID != group || due[0].DWSUID != "507523443" || !strings.HasPrefix(due[0].ConversationID, "cid-") {
		t.Fatalf("due = %+v %v", due, err)
	}
}
