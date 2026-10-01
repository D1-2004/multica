package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dws"
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

// fakeSupervisor is the supervisor's DWS session toward DEAP; it answers the
// whole {success, data} payload, as CallRaw returns it.
type fakeSupervisor struct {
	employeeUserID string
	code           string
	refuse         bool
	calls          []string
	args           []map[string]any
}

func (f *fakeSupervisor) CallRaw(_ context.Context, server dws.Server, tool string, args any) (json.RawMessage, error) {
	if server != dws.ServerDEAP {
		return nil, errors.New("not DEAP")
	}
	f.calls = append(f.calls, tool)
	f.args = append(f.args, args.(map[string]any))
	if f.refuse {
		return json.RawMessage(`{"success":false,"errorCode":"NO_PERMISSION","errorMsg":"not the supervisor"}`), nil
	}
	switch tool {
	case "get_digital_employee_detail":
		return json.RawMessage(`{"success":true,"data":{"profile":{"userId":"` + f.employeeUserID + `","corpId":"ding8196"}}}`), nil
	case "get_dws_auth_code":
		if f.code == "" {
			return json.RawMessage(`{"success":true,"data":{}}`), nil
		}
		return json.RawMessage(`{"success":true,"data":{"dwsClientId":"dingapp","dwsAuthCode":"` + f.code + `"}}`), nil
	}
	return nil, errors.New("unknown tool")
}

func (f *fakeSupervisor) Token() dws.Token { return dws.Token{ClientID: "dingapp"} }

// A digital employee's event credential comes from DEAP through its
// supervisor, whose credential our Agent Identity issues; other identities
// keep the usual mint.
func TestNativeSubscriptionMintThroughDEAP(t *testing.T) {
	linked := linkFor(nativeTestAgent, "406516560", "439446171")
	for _, tt := range []struct {
		name       string
		link       bool
		version    string
		employeeID string
		code       string
		refuse     bool
		wantErr    string
		wantBase   []string
		wantCalls  []string
	}{
		{name: "no link keeps the usual mint", wantBase: []string{"406516560"}},
		{name: "linked", link: true, version: deapCredentialVersion(linked), employeeID: "406516560", code: "code-1",
			wantBase: []string{deapTestSupervisor}, wantCalls: []string{"get_digital_employee_detail", "get_dws_auth_code"}},
		{name: "the linked employee is another account", link: true, version: deapCredentialVersion(linked), employeeID: "999", code: "code-1",
			wantErr: "not this identity", wantBase: []string{deapTestSupervisor}, wantCalls: []string{"get_digital_employee_detail"}},
		{name: "DEAP returns no code", link: true, version: deapCredentialVersion(linked), employeeID: "406516560",
			wantErr: "no DWS auth code", wantBase: []string{deapTestSupervisor}, wantCalls: []string{"get_digital_employee_detail", "get_dws_auth_code"}},
		{name: "DEAP refuses the supervisor", link: true, version: deapCredentialVersion(linked), refuse: true,
			wantErr: "NO_PERMISSION", wantBase: []string{deapTestSupervisor}, wantCalls: []string{"get_digital_employee_detail"}},
		// The sweep's version and the link disagree: wait for the next sweep
		// instead of storing a token under the wrong version.
		{name: "a link the sweep has not seen yet", link: true, wantErr: "changed"},
		{name: "a link removed since the sweep", version: deapCredentialVersion(linked), wantErr: "changed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			employee := dwsclient.Identity{AgentID: nativeTestAgent, UID: "406516560", OrgID: "439446171", CredentialVersion: tt.version}
			links := &fakeDEAPLinks{links: map[string]db.AgentDwsNativeDeapLink{}}
			if tt.link {
				links.links[nativeTestAgent] = linked
			}
			supervisor := &fakeSupervisor{employeeUserID: tt.employeeID, code: tt.code, refuse: tt.refuse}
			var opened []dwsclient.Identity
			h := &Handler{dwsNativeDEAPLinks: links,
				nativeDEAPSupervisor: func(ctx context.Context, id dwsclient.Identity, mint func(context.Context) (dwsclient.Credential, error)) (deapSupervisor, error) {
					opened = append(opened, id)
					if _, err := mint(ctx); err != nil {
						return nil, err
					}
					return supervisor, nil
				}}
			var minted []string
			base := func(_ context.Context, id dwsclient.Identity) (dwsclient.Credential, error) {
				minted = append(minted, id.UID)
				if id.OrgID != "439446171" || id.AgentID != nativeTestAgent {
					t.Errorf("minted %+v", id)
				}
				return dwsclient.Credential{UID: id.UID, ClientID: "dingapp", AuthCode: "agent-identity-code"}, nil
			}
			got, err := h.NativeSubscriptionMint(base, dwsclient.Shared{})(context.Background(), employee)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if strings.Join(minted, ",") != strings.Join(tt.wantBase, ",") || strings.Join(supervisor.calls, ",") != strings.Join(tt.wantCalls, ",") {
				t.Fatalf("minted %v, DEAP calls %v", minted, supervisor.calls)
			}
			switch {
			case tt.wantErr != "":
			case !tt.link:
				if got.AuthCode != "agent-identity-code" {
					t.Fatalf("credential = %+v", got)
				}
			default:
				if got != (dwsclient.Credential{UID: "406516560", ClientID: "dingapp", AuthCode: "code-1",
					ExpectUserID: "406516560", ExpectCorpID: "ding8196"}) {
					t.Fatalf("credential = %+v", got)
				}
				if opened[0] != (dwsclient.Identity{AgentID: nativeTestAgent, UID: deapTestSupervisor, OrgID: "439446171"}) {
					t.Fatalf("supervisor session = %+v", opened[0])
				}
				if supervisor.args[1]["agentUuid"] != deapTestEmployee || supervisor.args[1]["clientId"] != "dingapp" ||
					supervisor.args[0]["snapshot"] != "published" {
					t.Fatalf("DEAP args = %v", supervisor.args)
				}
			}
		})
	}
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
