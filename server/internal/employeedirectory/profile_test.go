package employeedirectory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/pkg/dws"
)

func knownSelf() (SelfEntry, error) {
	return SelfEntry{UserID: "507523443",
		Supervisor: Observed{State: ObservedKnown, Value: "朱鸿", Ref: "dingtalk:44675729:staff_id:024083"},
		Department: Observed{State: ObservedKnown, Value: "Real"},
		Title:      Observed{State: ObservedKnown, Value: "数字员工"}}, nil
}

// Two replicas race for the same agent: exactly one claims, the other sees
// the lease (and later the fresh facts) and reads nothing from DingTalk.
func TestProfileRefreshLeaseTwoReplicas(t *testing.T) {
	a, b := testSchema(t)
	ctx := context.Background()
	target := newProfileTarget()

	// Concurrent claims through two pools: one winner.
	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, pool := range []*Store{NewStore(a), NewStore(b), NewStore(a), NewStore(b)} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			_, ok, err := s.ClaimProfile(ctx, target, DefaultSchedule)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(pool)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("concurrent claims won = %d, want 1", wins)
	}

	// A full refresh on replica A holds the lease while it reads; replica B
	// neither claims nor calls DingTalk, before or after A completes.
	target = newProfileTarget()
	dirA := &fakeDirectory{self: knownSelf, block: make(chan struct{}), entered: make(chan struct{}, 1)}
	dirB := &fakeDirectory{self: knownSelf}
	replicaA := &Refresher{Store: NewStore(a), Directory: dirA}
	replicaB := &Refresher{Store: NewStore(b), Directory: dirB}
	done := make(chan Report, 1)
	go func() {
		rep, err := replicaA.RefreshProfile(ctx, target)
		if err != nil {
			t.Error(err)
		}
		done <- rep
	}()
	<-dirA.entered
	if rep, err := replicaB.RefreshProfile(ctx, target); err != nil || rep.Outcome != OutcomeNotDue {
		t.Fatalf("replica B while A holds the lease: %+v %v", rep, err)
	}
	close(dirA.block)
	if rep := <-done; rep.Outcome != OutcomeRefreshed {
		t.Fatalf("replica A: %+v", rep)
	}
	if rep, err := replicaB.RefreshProfile(ctx, target); err != nil || rep.Outcome != OutcomeNotDue {
		t.Fatalf("replica B after A refreshed: %+v %v", rep, err)
	}
	if dirB.n("self") != 0 {
		t.Fatalf("replica B read DingTalk %d times", dirB.n("self"))
	}

	// A crashed holder: its lease expires, B takes over, and A's late
	// completion is dropped.
	expireProfile(t, a, target)
	storeA, storeB := NewStore(a), NewStore(b)
	leaseA, ok, err := storeA.ClaimProfile(ctx, target, DefaultSchedule)
	if err != nil || !ok {
		t.Fatalf("claim A: %v %v", ok, err)
	}
	if _, err := a.Exec(ctx, `UPDATE employee_agent_profile_fact SET lease_until = now() - interval '1 second' WHERE agent_id = $1::uuid AND fact_key = 'supervisor'`, target.AgentID); err != nil {
		t.Fatal(err)
	}
	leaseB, ok, err := storeB.ClaimProfile(ctx, target, DefaultSchedule)
	if err != nil || !ok || leaseB == leaseA {
		t.Fatalf("claim B after expiry: %v %v", ok, err)
	}
	other := SelfEntry{Supervisor: Observed{State: ObservedKnown, Value: "李雷"}, Department: Observed{State: ObservedKnown, Value: "新部门"}, Title: Observed{State: ObservedUnregistered}}
	if err := storeB.CompleteProfile(ctx, target, leaseB, other); err != nil {
		t.Fatal(err)
	}
	stale, _ := knownSelf()
	if err := storeA.CompleteProfile(ctx, target, leaseA, stale); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("late completion: %v, want ErrLeaseLost", err)
	}
	p, found, err := storeA.ReadProfile(ctx, target.WorkspaceID, target.AgentID, target.TenantOrgID)
	if err != nil || !found || p.Supervisor.Value != "李雷" || p.Department.Value != "新部门" || p.Title.Status != StatusUnregistered {
		t.Fatalf("profile after takeover = %+v %v %v", p, found, err)
	}
}

// No supervisor in the address book is a stored fact, rendered explicitly.
func TestProfileUnknownSupervisorExplicit(t *testing.T) {
	a, _ := testSchema(t)
	ctx := context.Background()
	target := newProfileTarget()
	dir := &fakeDirectory{self: func() (SelfEntry, error) {
		return SelfEntry{UserID: target.DWSUID, Supervisor: Observed{State: ObservedUnregistered},
			Department: Observed{State: ObservedKnown, Value: "Real"}, Title: Observed{State: ObservedUnregistered}}, nil
	}}
	r := &Refresher{Store: NewStore(a), Directory: dir}
	rep, err := r.RefreshProfile(ctx, target)
	if err != nil || rep.Outcome != OutcomeRefreshed || rep.Supervisor != "unregistered" {
		t.Fatalf("refresh: %+v %v", rep, err)
	}
	p, found, err := r.Store.ReadProfile(ctx, target.WorkspaceID, target.AgentID, target.TenantOrgID)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if p.Supervisor.Status != StatusUnregistered || p.Supervisor.Value != "" || p.Supervisor.RefreshedAt.IsZero() {
		t.Fatalf("supervisor = %+v", p.Supervisor)
	}
	line := RenderSelfFacts(p, found, target.DWSUID)
	if !strings.Contains(line, "直属主管 通讯录未登记") || !strings.Contains(line, "部门 Real") || !strings.Contains(line, "职位 通讯录未登记") {
		t.Fatalf("line = %q", line)
	}
	// Nothing read yet, or read as another identity: unread, never guessed.
	if got := RenderSelfFacts(Profile{}, false, target.DWSUID); got != SelfFactsUnread {
		t.Fatalf("unread line = %q", got)
	}
	if got := RenderSelfFacts(p, true, "another-uid"); got != SelfFactsUnread {
		t.Fatalf("other identity line = %q", got)
	}
}

// A failed refresh keeps every stored value, records the reason, and backs
// off instead of retrying on every tick.
func TestProfileErrorKeepsOldValue(t *testing.T) {
	a, _ := testSchema(t)
	ctx := context.Background()
	target := newProfileTarget()
	dir := &fakeDirectory{self: knownSelf}
	r := &Refresher{Store: NewStore(a), Directory: dir}
	if rep, err := r.RefreshProfile(ctx, target); err != nil || rep.Outcome != OutcomeRefreshed {
		t.Fatalf("first refresh: %+v %v", rep, err)
	}
	before, _, _ := r.Store.ReadProfile(ctx, target.WorkspaceID, target.AgentID, target.TenantOrgID)

	expireProfile(t, a, target)
	dir.self = func() (SelfEntry, error) {
		return SelfEntry{}, &dws.Error{Server: dws.ServerContact, Tool: "get_user_info_by_user_ids", Kind: dws.KindBusiness, Code: "FORBIDDEN"}
	}
	rep, err := r.RefreshProfile(ctx, target)
	if err != nil || rep.Outcome != OutcomeFailed || rep.ErrorCode != "forbidden" {
		t.Fatalf("failed refresh: %+v %v", rep, err)
	}
	after, found, err := r.Store.ReadProfile(ctx, target.WorkspaceID, target.AgentID, target.TenantOrgID)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if after.Supervisor.Value != "朱鸿" || after.Supervisor.Status != StatusKnown || after.Department.Value != "Real" || after.Title.Value != "数字员工" {
		t.Fatalf("values changed after a failed refresh: %+v", after)
	}
	if after.ErrorCode != "forbidden" || !after.RefreshedAt.Before(before.RefreshedAt) {
		t.Fatalf("error not recorded: %+v", after)
	}
	if line := RenderSelfFacts(after, true, target.DWSUID); !strings.Contains(line, "直属主管 朱鸿") {
		t.Fatalf("old value not rendered: %q", line)
	}
	// Backoff: an immediate retry is not due.
	calls := dir.n("self")
	if rep, err := r.RefreshProfile(ctx, target); err != nil || rep.Outcome != OutcomeNotDue || dir.n("self") != calls {
		t.Fatalf("retry inside backoff: %+v %v", rep, err)
	}

	// A read that cannot tell one fact (the fallback carries no title) keeps
	// that fact's stored value.
	expireProfile(t, a, target)
	dir.self = func() (SelfEntry, error) {
		e, _ := knownSelf()
		e.Title = Observed{State: ObservedUnavailable}
		e.Department = Observed{State: ObservedKnown, Value: "Real 二部"}
		return e, nil
	}
	if rep, err := r.RefreshProfile(ctx, target); err != nil || rep.Outcome != OutcomeRefreshed {
		t.Fatalf("partial refresh: %+v %v", rep, err)
	}
	partial, _, _ := r.Store.ReadProfile(ctx, target.WorkspaceID, target.AgentID, target.TenantOrgID)
	if partial.Title.Value != "数字员工" || partial.Title.ErrorCode != "unavailable" || partial.Department.Value != "Real 二部" || partial.ErrorCode != "" {
		t.Fatalf("partial = %+v", partial)
	}
}

// A changed identity refreshes at once and never inherits the old
// identity's values.
func TestProfileIdentityChangeRefreshesAndDropsOldValues(t *testing.T) {
	a, _ := testSchema(t)
	ctx := context.Background()
	target := newProfileTarget()
	dir := &fakeDirectory{self: knownSelf}
	r := &Refresher{Store: NewStore(a), Directory: dir}
	if _, err := r.RefreshProfile(ctx, target); err != nil {
		t.Fatal(err)
	}
	rebound := target
	rebound.DWSUID = "600000001"
	dir.self = func() (SelfEntry, error) {
		return SelfEntry{UserID: rebound.DWSUID, Supervisor: Observed{State: ObservedUnregistered},
			Department: Observed{State: ObservedKnown, Value: "新组"}, Title: Observed{State: ObservedUnavailable}}, nil
	}
	if rep, err := r.RefreshProfile(ctx, rebound); err != nil || rep.Outcome != OutcomeRefreshed {
		t.Fatalf("rebound refresh: %+v %v", rep, err)
	}
	p, _, _ := r.Store.ReadProfile(ctx, target.WorkspaceID, target.AgentID, target.TenantOrgID)
	if p.DWSUID != rebound.DWSUID || p.Title.Status != StatusPending || p.Title.Value != "" || p.Supervisor.Status != StatusUnregistered {
		t.Fatalf("rebound profile = %+v", p)
	}
	if line := RenderSelfFacts(p, true, rebound.DWSUID); !strings.Contains(line, "职位 未读取") || strings.Contains(line, "数字员工") {
		t.Fatalf("line = %q", line)
	}
}

// Only employee-mode, enabled, unarchived agents with an identity are
// scanned, and only when due.
func TestDueProfileTargetsOnlyEmployeeModeAndDue(t *testing.T) {
	a, _ := testSchema(t)
	ctx := context.Background()
	ws := uuid.NewString()
	mk := func(mode string, enabled bool, archived bool) string {
		id := uuid.NewString()
		archivedAt := "NULL"
		if archived {
			archivedAt = "now()"
		}
		if _, err := a.Exec(ctx, `INSERT INTO agent (id, workspace_id, coordination_mode, inbound_coordinator, archived_at) VALUES ($1, $2, $3, $4, `+archivedAt+`)`, id, ws, mode, enabled); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Exec(ctx, `INSERT INTO agent_dingtalk_identity (agent_id, workspace_id, dws_uid, org_id) VALUES ($1, $2, $3, '44675729')`, id, ws, "uid-"+id[:8]); err != nil {
			t.Fatal(err)
		}
		return id
	}
	employee := mk("employee", true, false)
	mk("coordinator", true, false)
	mk("employee", false, false)
	mk("employee", true, true)
	store := NewStore(a)
	due, err := store.DueProfileTargets(ctx, DefaultSchedule, 10)
	if err != nil || len(due) != 1 || due[0].AgentID != employee || due[0].TenantOrgID != "44675729" {
		t.Fatalf("due = %+v %v", due, err)
	}
	r := &Refresher{Store: store, Directory: &fakeDirectory{self: knownSelf}}
	if _, err := r.RefreshProfile(ctx, due[0]); err != nil {
		t.Fatal(err)
	}
	if due, err = store.DueProfileTargets(ctx, DefaultSchedule, 10); err != nil || len(due) != 0 {
		t.Fatalf("due after refresh = %+v %v", due, err)
	}
}
