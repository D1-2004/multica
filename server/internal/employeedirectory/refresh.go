package employeedirectory

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/dwsclient"
)

// Refresher runs one refresh at a time under the Store's leases: claim
// (PostgreSQL), read DingTalk (no lock held), then complete or fail under
// the same lease. No model is ever called.
type Refresher struct {
	Store     *Store
	Directory Directory
	Schedule  Schedule
	// StoreMax bounds the members kept per roster (default 200).
	StoreMax int
	// SearchBudget bounds address book searches per roster refresh (default 30).
	SearchBudget int
	// FactMax bounds the proved members whose title/department are read
	// (default 60).
	FactMax int
	// KeptMaxAge is how long a kept openDingTalkId→staffId proof is trusted
	// without a new search (default 7 days, as the native sender path).
	KeptMaxAge time.Duration
	// Remember keeps a staffId newly proved for an openDingTalkId
	// (contextcap.RememberOpenIDStaff); nil keeps nothing.
	Remember func(ctx context.Context, orgID, viewerUID, openDingTalkID, staffID string) error
	// Observe receives every finished refresh (logs, Langfuse); nil skips.
	Observe func(Report)
}

// Outcomes of one refresh.
const (
	OutcomeRefreshed = "refreshed"
	OutcomeNotDue    = "not_due"
	OutcomeFailed    = "failed"
	OutcomeLeaseLost = "lease_lost"
)

// Report describes one refresh, metadata only (no names or facts).
type Report struct {
	Kind        string // "profile" or "roster"
	WorkspaceID string
	AgentID     string
	SceneID     string
	TenantOrgID string
	Outcome     string
	ErrorCode   string
	Elapsed     time.Duration
	// Profile: the stored status of each fact after the refresh.
	Supervisor, Department, Title string
	// Roster counts.
	Total, Stored, SameOrg, Other, Unknown, Searches, FactsRead int
}

func (r *Refresher) observe(rep Report, started time.Time) Report {
	rep.Elapsed = time.Since(started)
	if r.Observe != nil {
		r.Observe(rep)
	}
	return rep
}

// writeCtx bounds a completion that must land even after the read's
// deadline passed.
func writeCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func observedStatus(o Observed) string {
	switch o.State {
	case ObservedKnown:
		return string(StatusKnown)
	case ObservedUnregistered:
		return string(StatusUnregistered)
	default:
		return "unavailable"
	}
}

// RefreshProfile refreshes the agent's own facts when they are due.
func (r *Refresher) RefreshProfile(ctx context.Context, t ProfileTarget) (Report, error) {
	started := time.Now()
	rep := Report{Kind: "profile", WorkspaceID: t.WorkspaceID, AgentID: t.AgentID, TenantOrgID: t.TenantOrgID}
	lease, ok, err := r.Store.ClaimProfile(ctx, t, r.Schedule)
	if err != nil {
		return rep, err
	}
	if !ok {
		rep.Outcome = OutcomeNotDue
		return rep, nil
	}
	if r.Directory == nil {
		err = errors.New("directory unavailable")
	}
	var entry SelfEntry
	if err == nil {
		entry, err = r.Directory.Self(ctx, dwsclient.Identity{AgentID: t.AgentID, UID: t.DWSUID, OrgID: t.TenantOrgID})
	}
	wctx, cancel := writeCtx(ctx)
	defer cancel()
	if err != nil {
		rep.Outcome, rep.ErrorCode = OutcomeFailed, errorCode(classify(err))
		if r.Directory == nil {
			rep.ErrorCode = "unavailable"
		}
		if ferr := r.Store.FailProfile(wctx, t, lease, rep.ErrorCode); errors.Is(ferr, ErrLeaseLost) {
			rep.Outcome = OutcomeLeaseLost
		} else if ferr != nil {
			return r.observe(rep, started), ferr
		}
		return r.observe(rep, started), nil
	}
	if err := r.Store.CompleteProfile(wctx, t, lease, entry); errors.Is(err, ErrLeaseLost) {
		rep.Outcome = OutcomeLeaseLost
		return r.observe(rep, started), nil
	} else if err != nil {
		return r.observe(rep, started), err
	}
	rep.Outcome = OutcomeRefreshed
	rep.Supervisor, rep.Department, rep.Title = observedStatus(entry.Supervisor), observedStatus(entry.Department), observedStatus(entry.Title)
	return r.observe(rep, started), nil
}

func (r *Refresher) bounds() (storeMax, searches, facts int, keptAge time.Duration) {
	storeMax, searches, facts, keptAge = r.StoreMax, r.SearchBudget, r.FactMax, r.KeptMaxAge
	if storeMax <= 0 {
		storeMax = 200
	}
	if searches <= 0 {
		searches = 30
	}
	if facts <= 0 {
		facts = 60
	}
	if keptAge <= 0 {
		keptAge = 7 * 24 * time.Hour
	}
	return
}

// orderMembers dedupes the group's members and orders them owner, admins,
// then the list order, keeping at most max.
func orderMembers(in []GroupMember, max int) ([]GroupMember, int) {
	seen := map[string]bool{}
	var owners, admins, rest []GroupMember
	for _, m := range in {
		m.OpenDingTalkID = cleanID(m.OpenDingTalkID)
		if m.OpenDingTalkID == "" || seen[m.OpenDingTalkID] {
			continue
		}
		seen[m.OpenDingTalkID] = true
		switch m.Role {
		case RoleOwner:
			owners = append(owners, m)
		case RoleAdmin:
			admins = append(admins, m)
		default:
			m.Role = RoleMember
			rest = append(rest, m)
		}
	}
	all := append(append(owners, admins...), rest...)
	total := len(all)
	if len(all) > max {
		all = all[:max]
	}
	return all, total
}

// RefreshRoster rebuilds a group scene's roster when it is due. A member is
// same-org only when the agent's own org address book proves their staffId
// for exactly their openDingTalkId; only then are title and department
// read. A failed read keeps the previous roster; a member whose check fails
// keeps its previous classification.
func (r *Refresher) RefreshRoster(ctx context.Context, t RosterTarget) (Report, error) {
	started := time.Now()
	rep := Report{Kind: "roster", WorkspaceID: t.WorkspaceID, AgentID: t.AgentID, SceneID: t.SceneID, TenantOrgID: t.TenantOrgID}
	lease, ok, err := r.Store.ClaimRoster(ctx, t, r.Schedule)
	if err != nil {
		return rep, err
	}
	if !ok {
		rep.Outcome = OutcomeNotDue
		return rep, nil
	}
	members, total, err := r.buildRoster(ctx, t, &rep)
	wctx, cancel := writeCtx(ctx)
	defer cancel()
	if err != nil {
		rep.Outcome, rep.ErrorCode = OutcomeFailed, errorCode(classify(err))
		if r.Directory == nil {
			rep.ErrorCode = "unavailable"
		}
		if ferr := r.Store.FailRoster(wctx, t, lease, rep.ErrorCode); errors.Is(ferr, ErrLeaseLost) {
			rep.Outcome = OutcomeLeaseLost
		} else if ferr != nil {
			return r.observe(rep, started), ferr
		}
		return r.observe(rep, started), nil
	}
	if err := r.Store.CompleteRoster(wctx, t, lease, members, total); errors.Is(err, ErrLeaseLost) {
		rep.Outcome = OutcomeLeaseLost
		return r.observe(rep, started), nil
	} else if err != nil {
		return r.observe(rep, started), err
	}
	rep.Outcome = OutcomeRefreshed
	return r.observe(rep, started), nil
}

func (r *Refresher) buildRoster(ctx context.Context, t RosterTarget, rep *Report) ([]Member, int, error) {
	if r.Directory == nil {
		return nil, 0, errors.New("directory unavailable")
	}
	id := dwsclient.Identity{AgentID: t.AgentID, UID: t.DWSUID, OrgID: t.TenantOrgID}
	listed, err := r.Directory.GroupMembers(ctx, id, t.ConversationID)
	if err != nil {
		return nil, 0, err
	}
	storeMax, searchBudget, factMax, keptAge := r.bounds()
	ordered, total := orderMembers(listed, storeMax)
	rep.Total, rep.Stored = total, len(ordered)

	previous := map[string]Member{}
	if old, ok, err := r.Store.ReadRoster(ctx, t.WorkspaceID, t.AgentID, t.SceneID); err == nil && ok && old.TenantOrgID == t.TenantOrgID && old.DWSUID == t.DWSUID {
		for _, m := range old.Members {
			previous[m.Ref] = m
		}
	}
	openIDs := make([]string, 0, len(ordered))
	for _, m := range ordered {
		openIDs = append(openIDs, m.OpenDingTalkID)
	}
	kept, err := r.Store.keptStaff(ctx, t.TenantOrgID, t.DWSUID, openIDs, keptAge)
	if err != nil {
		return nil, 0, err
	}

	out := make([]Member, len(ordered))
	staffOf := make([]string, len(ordered))
	for i, m := range ordered {
		out[i] = Member{Ref: openRef(t.TenantOrgID, m.OpenDingTalkID), Name: publicText(m.Name, maxNameRunes),
			GroupNick: publicText(m.GroupNick, maxNameRunes), Role: m.Role, Org: OrgUnknown}
		if out[i].Name == "" {
			out[i].Name, out[i].GroupNick = out[i].GroupNick, ""
		}
		if out[i].Name == "" {
			out[i].Name = "未显示名的成员"
		}
		if staff := kept[m.OpenDingTalkID]; staff != "" {
			staffOf[i] = staff
			out[i].Org = OrgSame
		}
	}
	// Search order: never checked first, recently found in another org last,
	// so successive daily refreshes cover different members.
	var searchOrder []int
	for pass := 0; pass < 2; pass++ {
		for i := range ordered {
			if staffOf[i] != "" {
				continue
			}
			wasOther := previous[out[i].Ref].Org == OrgOther
			if (pass == 0) != wasOther {
				searchOrder = append(searchOrder, i)
			}
		}
	}
	searchFailed := false
	for _, i := range searchOrder {
		if rep.Searches >= searchBudget || searchFailed || ctx.Err() != nil {
			break
		}
		m := ordered[i]
		rep.Searches++
		staff, err := r.Directory.ProveStaff(ctx, id, m.OpenDingTalkID, []string{m.Name, m.GroupNick})
		if err != nil {
			// Stop searching on the first failure: the rest keep their
			// previous classification.
			searchFailed = true
			continue
		}
		if staff = cleanID(staff); staff == "" {
			out[i].Org = OrgOther
			continue
		}
		staffOf[i], out[i].Org = staff, OrgSame
		if r.Remember != nil {
			_ = r.Remember(ctx, t.TenantOrgID, t.DWSUID, m.OpenDingTalkID, staff)
		}
	}
	// Members not decided this time keep their previous answer.
	for i := range out {
		if out[i].Org != OrgUnknown {
			continue
		}
		if prev, ok := previous[out[i].Ref]; ok && prev.Org != OrgUnknown {
			out[i].Org = prev.Org
			if prev.Org == OrgSame {
				staffOf[i] = strings.TrimPrefix(prev.StaffRef, "dingtalk:"+t.TenantOrgID+":staff_id:")
				if cleanID(staffOf[i]) == "" {
					staffOf[i], out[i].Org = "", OrgUnknown
				}
			}
		}
	}
	var factIDs []string
	for i := range out {
		if staffOf[i] == "" {
			continue
		}
		out[i].StaffRef = staffRef(t.TenantOrgID, staffOf[i])
		if staffOf[i] == cleanID(t.DWSUID) {
			out[i].Self = true
			continue
		}
		if len(factIDs) < factMax {
			factIDs = append(factIDs, staffOf[i])
		}
	}
	facts := map[string]Person{}
	factsRead := false
	if len(factIDs) > 0 {
		if people, err := r.Directory.Users(ctx, id, factIDs); err == nil {
			factsRead = true
			for _, p := range people {
				facts[p.StaffID] = p
			}
		}
	}
	rep.FactsRead = len(facts)
	for i := range out {
		if out[i].Org != OrgSame || out[i].Self {
			continue
		}
		if p, ok := facts[staffOf[i]]; ok {
			out[i].Title, out[i].Department = publicText(p.Title, maxTitleRunes), publicText(p.Department, maxDeptRunes)
		} else if prev, ok := previous[out[i].Ref]; ok && (!factsRead || prev.StaffRef == out[i].StaffRef) && prev.Org == OrgSame {
			// The read failed or skipped this member: keep the older facts.
			out[i].Title, out[i].Department = prev.Title, prev.Department
		}
	}
	for _, m := range out {
		switch m.Org {
		case OrgSame:
			rep.SameOrg++
		case OrgOther:
			rep.Other++
		default:
			rep.Unknown++
		}
	}
	return out, total, nil
}
