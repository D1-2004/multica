package dwsidentity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dws"
)

const (
	testAgent      = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	testEmployee   = "e6b9cb47-74ff-4d7d-b46d-123bdb9fbecb"
	testSupervisor = "6753994909"
)

type fakeLinks struct {
	links map[string]db.AgentDwsNativeDeapLink
	err   error
}

func (f fakeLinks) GetDWSNativeDEAPLink(_ context.Context, p db.GetDWSNativeDEAPLinkParams) (db.AgentDwsNativeDeapLink, error) {
	if f.err != nil {
		return db.AgentDwsNativeDeapLink{}, f.err
	}
	link, ok := f.links[util.UUIDToString(p.AgentID)]
	if !ok || link.DwsUid != p.DwsUid || link.OrgID != p.OrgID {
		return db.AgentDwsNativeDeapLink{}, pgx.ErrNoRows
	}
	return link, nil
}

// fakeSupervisor answers DEAP's whole {success, data} payload, as CallRaw
// returns it.
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

func link() db.AgentDwsNativeDeapLink {
	return db.AgentDwsNativeDeapLink{AgentID: util.MustParseUUID(testAgent), DwsUid: "858957531", OrgID: "439446171",
		DeapAgentUuid: testEmployee, SupervisorUid: testSupervisor}
}

// A linked digital employee is issued by DEAP through its supervisor, whose
// credential the caller's Agent Identity mint issues; every other identity
// keeps the caller's mint.
func TestProviderIssuesLinkedEmployeesThroughDEAP(t *testing.T) {
	employee := dwsclient.Identity{AgentID: testAgent, UID: "858957531", OrgID: "439446171"}
	for _, tt := range []struct {
		name       string
		linked     bool
		employeeID string
		code       string
		refuse     bool
		wantErr    string
		wantBase   []string
		wantCalls  []string
	}{
		{name: "no link keeps the caller's mint", wantBase: []string{"858957531"}},
		{name: "linked", linked: true, employeeID: "858957531", code: "code-1",
			wantBase: []string{testSupervisor}, wantCalls: []string{"get_digital_employee_detail", "get_dws_auth_code"}},
		{name: "the linked employee is another account", linked: true, employeeID: "999", code: "code-1",
			wantErr: "not this identity", wantBase: []string{testSupervisor}, wantCalls: []string{"get_digital_employee_detail"}},
		{name: "DEAP returns no code", linked: true, employeeID: "858957531",
			wantErr: "no DWS auth code", wantBase: []string{testSupervisor}, wantCalls: []string{"get_digital_employee_detail", "get_dws_auth_code"}},
		{name: "DEAP refuses the supervisor", linked: true, refuse: true,
			wantErr: "NO_PERMISSION", wantBase: []string{testSupervisor}, wantCalls: []string{"get_digital_employee_detail"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			links := fakeLinks{links: map[string]db.AgentDwsNativeDeapLink{}}
			if tt.linked {
				links.links[testAgent] = link()
			}
			supervisor := &fakeSupervisor{employeeUserID: tt.employeeID, code: tt.code, refuse: tt.refuse}
			var opened []dwsclient.Identity
			p := &Provider{Links: links, OpenSupervisor: func(ctx context.Context, _ dwsclient.Shared, id dwsclient.Identity, base dwsclient.IdentityMint) (Supervisor, error) {
				opened = append(opened, id)
				if _, err := base(ctx, id); err != nil {
					return nil, err
				}
				return supervisor, nil
			}}
			var minted []string
			base := func(_ context.Context, id dwsclient.Identity) (dwsclient.Credential, error) {
				minted = append(minted, id.UID)
				if id.OrgID != "439446171" || id.AgentID != testAgent {
					t.Errorf("minted %+v", id)
				}
				return dwsclient.Credential{UID: id.UID, ClientID: "dingapp", AuthCode: "agent-identity-code"}, nil
			}
			resolved, mint, err := p.Resolve(context.Background(), dwsclient.Shared{}, employee, base)
			if err != nil {
				t.Fatal(err)
			}
			if tt.linked != (resolved.CredentialVersion == Version(link())) || (!tt.linked && resolved.CredentialVersion != "") {
				t.Fatalf("resolved %+v", resolved)
			}
			got, err := mint(context.Background())
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
			case !tt.linked:
				if got.AuthCode != "agent-identity-code" {
					t.Fatalf("credential = %+v", got)
				}
			default:
				if got != (dwsclient.Credential{UID: "858957531", ClientID: "dingapp", AuthCode: "code-1",
					ExpectUserID: "858957531", ExpectCorpID: "ding8196"}) {
					t.Fatalf("credential = %+v", got)
				}
				if opened[0] != (dwsclient.Identity{AgentID: testAgent, UID: testSupervisor, OrgID: "439446171"}) {
					t.Fatalf("supervisor session = %+v", opened[0])
				}
				if supervisor.args[1]["agentUuid"] != testEmployee || supervisor.args[1]["clientId"] != "dingapp" ||
					supervisor.args[0]["snapshot"] != "published" {
					t.Fatalf("DEAP args = %v", supervisor.args)
				}
			}
		})
	}
}

// Unreadable links stop the session instead of issuing the wrong principal;
// identities a link was not made for, and non-agent identities, are usual.
func TestProviderRefusesWhenLinksAreUnreadable(t *testing.T) {
	boom := errors.New("db down")
	base := func(_ context.Context, id dwsclient.Identity) (dwsclient.Credential, error) {
		return dwsclient.Credential{UID: id.UID}, nil
	}
	p := &Provider{Links: fakeLinks{err: boom}}
	if _, _, err := p.Resolve(context.Background(), dwsclient.Shared{}, dwsclient.Identity{AgentID: testAgent, UID: "1", OrgID: "2"}, base); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	p = &Provider{Links: fakeLinks{links: map[string]db.AgentDwsNativeDeapLink{testAgent: link()}}}
	other, mint, err := p.Resolve(context.Background(), dwsclient.Shared{}, dwsclient.Identity{AgentID: testAgent, UID: "777", OrgID: "439446171", CredentialVersion: "stale"}, base)
	if err != nil || other.CredentialVersion != "" {
		t.Fatalf("resolved %+v %v", other, err)
	}
	if c, _ := mint(context.Background()); c.UID != "777" {
		t.Fatalf("credential = %+v", c)
	}
	if _, _, err := (&Provider{Links: fakeLinks{err: boom}}).Resolve(context.Background(), dwsclient.Shared{}, dwsclient.Identity{AgentID: "not-a-uuid", UID: "1", OrgID: "2"}, base); err != nil {
		t.Fatalf("a non-agent identity consulted links: %v", err)
	}
	// A link naming the employee as its own supervisor is refused, not
	// resolved into itself.
	self := link()
	self.SupervisorUid = self.DwsUid
	p = &Provider{Links: fakeLinks{links: map[string]db.AgentDwsNativeDeapLink{testAgent: self}}}
	if _, _, err := p.Resolve(context.Background(), dwsclient.Shared{}, dwsclient.Identity{AgentID: testAgent, UID: self.DwsUid, OrgID: self.OrgID}, base); err == nil {
		t.Fatal("a self-supervised link resolved")
	}
}
