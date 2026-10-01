package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	deapTestEmployee   = "de2a8cc1-413c-47f0-a79b-fede1b853847"
	deapTestSupervisor = "6753994909"
)

// fakeDEAPLinks is the DEAP link table.
type fakeDEAPLinks struct {
	links   map[string]db.AgentDwsNativeDeapLink
	upserts []db.UpsertDWSNativeDEAPLinkParams
	deletes int
}

func (f *fakeDEAPLinks) GetDWSNativeDEAPLink(_ context.Context, p db.GetDWSNativeDEAPLinkParams) (db.AgentDwsNativeDeapLink, error) {
	link, ok := f.links[util.UUIDToString(p.AgentID)]
	if !ok || link.DwsUid != p.DwsUid || link.OrgID != p.OrgID {
		return db.AgentDwsNativeDeapLink{}, pgx.ErrNoRows
	}
	return link, nil
}

func (f *fakeDEAPLinks) GetWorkspaceDWSNativeDEAPLink(_ context.Context, p db.GetWorkspaceDWSNativeDEAPLinkParams) (db.AgentDwsNativeDeapLink, error) {
	link, ok := f.links[util.UUIDToString(p.AgentID)]
	if !ok {
		return db.AgentDwsNativeDeapLink{}, pgx.ErrNoRows
	}
	return link, nil
}

func (f *fakeDEAPLinks) UpsertDWSNativeDEAPLink(_ context.Context, p db.UpsertDWSNativeDEAPLinkParams) (db.AgentDwsNativeDeapLink, error) {
	f.upserts = append(f.upserts, p)
	link := db.AgentDwsNativeDeapLink{AgentID: p.AgentID, WorkspaceID: p.WorkspaceID, DwsUid: p.DwsUid, OrgID: p.OrgID,
		DeapAgentUuid: p.DeapAgentUuid, SupervisorUid: p.SupervisorUid, UpdatedBy: p.UpdatedBy}
	if f.links == nil {
		f.links = map[string]db.AgentDwsNativeDeapLink{}
	}
	f.links[util.UUIDToString(p.AgentID)] = link
	return link, nil
}

func (f *fakeDEAPLinks) DeleteDWSNativeDEAPLink(_ context.Context, p db.DeleteDWSNativeDEAPLinkParams) error {
	f.deletes++
	delete(f.links, util.UUIDToString(p.AgentID))
	return nil
}

func (f *fakeDEAPLinks) ListDWSNativeDEAPLinks(context.Context) ([]db.AgentDwsNativeDeapLink, error) {
	out := make([]db.AgentDwsNativeDeapLink, 0, len(f.links))
	for _, link := range f.links {
		out = append(out, link)
	}
	return out, nil
}

func linkFor(agent, uid, org string) db.AgentDwsNativeDeapLink {
	return db.AgentDwsNativeDeapLink{AgentID: parseUUID(agent), WorkspaceID: parseUUID(nativeTestWorkspace),
		DwsUid: uid, OrgID: org, DeapAgentUuid: deapTestEmployee, SupervisorUid: deapTestSupervisor}
}

// The native source versions an identity's credential by its DEAP link, so
// a link change mints and connects afresh; a link for another account is
// ignored.
func TestNativeSubscriptionIdentitiesCarryTheDEAPLink(t *testing.T) {
	other := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	store := &fakeNativeStore{enabled: map[string]bool{}, streaming: []db.ListActiveDWSNativeSubscriptionsRow{
		{AgentID: parseUUID(nativeTestAgent), DwsUid: "406516560", OrgID: "439446171"},
		{AgentID: parseUUID(other), DwsUid: "777", OrgID: "439446171"},
	}}
	links := &fakeDEAPLinks{links: map[string]db.AgentDwsNativeDeapLink{
		nativeTestAgent: linkFor(nativeTestAgent, "406516560", "439446171"),
		other:           linkFor(other, "888", "439446171"),
	}}
	h := &Handler{dwsNativeSubscriptions: store, dwsNativeDEAPLinks: links}
	ids, err := h.NativeSubscriptionIdentities(context.Background())
	if err != nil || len(ids) != 2 {
		t.Fatalf("ids = %+v, %v", ids, err)
	}
	if !strings.HasPrefix(ids[0].CredentialVersion, "deap-") || strings.Contains(ids[0].CredentialVersion, deapTestSupervisor) {
		t.Fatalf("linked version = %q", ids[0].CredentialVersion)
	}
	if ids[1].CredentialVersion != "" {
		t.Fatalf("a link for another account versioned %+v", ids[1])
	}
	changed := linkFor(nativeTestAgent, "406516560", "439446171")
	changed.SupervisorUid = "25698887"
	links.links[nativeTestAgent] = changed
	again, _ := h.NativeSubscriptionIdentities(context.Background())
	if again[0].CredentialVersion == ids[0].CredentialVersion {
		t.Fatal("a changed link keeps the credential version")
	}
}

func TestSetDWSNativeDEAPLink(t *testing.T) {
	path := "/api/workspaces/" + nativeTestWorkspace + "/dingtalk/account-bindings/" + nativeTestAgent + "/native-subscription/deap-link"
	valid := `{"deap_agent_uuid":"` + deapTestEmployee + `","supervisor_uid":"` + deapTestSupervisor + `"}`
	for _, tt := range []struct {
		name     string
		method   string
		body     string
		operator bool
		identity bool
		want     int
		code     string
	}{
		{name: "operator links", method: http.MethodPut, body: valid, operator: true, identity: true, want: 200},
		{name: "only operators", method: http.MethodPut, body: valid, identity: true, want: 403, code: "operator_only"},
		{name: "needs an identity", method: http.MethodPut, body: valid, operator: true, want: 409, code: "native_subscription_requires_identity"},
		{name: "employee id is checked", method: http.MethodPut, body: `{"deap_agent_uuid":"x","supervisor_uid":"1"}`, operator: true, identity: true,
			want: 400, code: "invalid_deap_link"},
		{name: "supervisor id is checked", method: http.MethodPut, body: `{"deap_agent_uuid":"` + deapTestEmployee + `","supervisor_uid":"ding1"}`,
			operator: true, identity: true, want: 400, code: "invalid_deap_link"},
		{name: "the employee cannot supervise itself", method: http.MethodPut, body: `{"deap_agent_uuid":"` + deapTestEmployee + `","supervisor_uid":"1001"}`,
			operator: true, identity: true, want: 400, code: "invalid_deap_link"},
		{name: "operator unlinks", method: http.MethodDelete, operator: true, identity: true, want: 200},
		{name: "only operators unlink", method: http.MethodDelete, identity: true, want: 403, code: "operator_only"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeNativeStore{enabled: map[string]bool{}, identity: tt.identity}
			links := &fakeDEAPLinks{links: map[string]db.AgentDwsNativeDeapLink{}}
			h := nativeTestHandler(store, &fakeDingTalkAccountBindingService{}, tt.operator)
			h.dwsNativeDEAPLinks = links
			w := httptest.NewRecorder()
			h.SetDWSNativeDEAPLink(w, nativeRequest(tt.method, path, tt.body))
			if w.Code != tt.want || (tt.code != "" && errorCode(t, w) != tt.code) {
				t.Fatalf("status = %d body = %s, want %d %s", w.Code, w.Body.String(), tt.want, tt.code)
			}
			switch {
			case tt.want != 200:
				if len(links.upserts) != 0 || links.deletes != 0 {
					t.Fatal("a refused request changed the link")
				}
			case tt.method == http.MethodPut:
				p := links.upserts[0]
				if p.DwsUid != "1001" || p.OrgID != "2002" || p.DeapAgentUuid != deapTestEmployee ||
					p.SupervisorUid != deapTestSupervisor || p.UpdatedBy != parseUUID(nativeTestUser) {
					t.Fatalf("upsert = %+v", p)
				}
			default:
				if links.deletes != 1 {
					t.Fatal("not unlinked")
				}
			}
		})
	}
}

// Everyone sees the identity's DEAP link; only operators may edit it.
func TestGetDWSNativeSubscriptionShowsTheDEAPLink(t *testing.T) {
	path := "/api/workspaces/" + nativeTestWorkspace + "/dingtalk/account-bindings/" + nativeTestAgent + "/native-subscription"
	for _, operator := range []bool{false, true} {
		store := &fakeNativeStore{enabled: map[string]bool{}, identity: true}
		links := &fakeDEAPLinks{links: map[string]db.AgentDwsNativeDeapLink{nativeTestAgent: linkFor(nativeTestAgent, "1001", "2002")}}
		h := nativeTestHandler(store, &fakeDingTalkAccountBindingService{}, operator)
		h.dwsNativeDEAPLinks = links
		h.nativeStreamStatus = &fakeNativeStreams{}
		w := httptest.NewRecorder()
		h.GetDWSNativeSubscription(w, nativeRequest(http.MethodGet, path, ""))
		var body struct {
			DEAPLink *struct {
				DeapAgentUUID string `json:"deap_agent_uuid"`
				SupervisorUID string `json:"supervisor_uid"`
			} `json:"deap_link"`
			Editable bool `json:"deap_link_editable"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.DEAPLink == nil ||
			body.DEAPLink.DeapAgentUUID != deapTestEmployee || body.DEAPLink.SupervisorUID != deapTestSupervisor || body.Editable != operator {
			t.Fatalf("operator=%v: %s", operator, w.Body.String())
		}
	}
	// A link made for the identity's previous account is not shown.
	store := &fakeNativeStore{enabled: map[string]bool{}, identity: true}
	links := &fakeDEAPLinks{links: map[string]db.AgentDwsNativeDeapLink{nativeTestAgent: linkFor(nativeTestAgent, "9999", "2002")}}
	h := nativeTestHandler(store, &fakeDingTalkAccountBindingService{}, true)
	h.dwsNativeDEAPLinks = links
	w := httptest.NewRecorder()
	h.GetDWSNativeSubscription(w, nativeRequest(http.MethodGet, path, ""))
	if !strings.Contains(w.Body.String(), `"deap_link":null`) {
		t.Fatalf("stale link shown: %s", w.Body.String())
	}
}
