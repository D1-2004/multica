package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/orgemphsf"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type taskContextQueriesStub struct {
	agent   db.Agent
	runtime db.AgentRuntime
}

func (s *taskContextQueriesStub) GetAgent(context.Context, pgtype.UUID) (db.Agent, error) {
	return s.agent, nil
}

func (s *taskContextQueriesStub) GetAgentRuntime(context.Context, pgtype.UUID) (db.AgentRuntime, error) {
	return s.runtime, nil
}

type robotEmployeeResolverStub struct {
	employee orgemphsf.Employee
	err      error
	corpID   string
	staffID  string
}

func (s *robotEmployeeResolverStub) ResolveEmployeeByCorpID(_ context.Context, corpID, staffID string) (orgemphsf.Employee, error) {
	s.corpID = corpID
	s.staffID = staffID
	return s.employee, s.err
}

func newRobotTaskContextResolver(employee *robotEmployeeResolverStub) *robotTaskContextResolver {
	return &robotTaskContextResolver{
		q: &taskContextQueriesStub{
			agent: db.Agent{RuntimeID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}},
			runtime: db.AgentRuntime{RuntimeMode: "cloud", Metadata: []byte(`{
				"kind":"fc-e2b",
				"capabilities":["dws"]
			}`)},
		},
		employees: employee,
	}
}

func taskContextMessage(t *testing.T, corpID, staffID string) channel.InboundMessage {
	t.Helper()
	raw, err := json.Marshal(dingtalkRawEvent{SenderCorpID: corpID, SenderStaffID: staffID})
	if err != nil {
		t.Fatalf("marshal raw message: %v", err)
	}
	return channel.InboundMessage{Raw: raw}
}

func TestRobotTaskContextResolverContinuesWithoutMissingOrganizationIdentity(t *testing.T) {
	employee := &robotEmployeeResolverStub{}
	resolver := newRobotTaskContextResolver(employee)
	contextJSON, err := resolver.ResolveTaskContext(context.Background(), engine.ResolvedInstallation{}, taskContextMessage(t, "", "Staff-A_106201"))
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	assertDingTalkIdentityUnavailable(t, contextJSON, protocol.DingTalkRobotIdentityUnavailableMissingOrg)
	if employee.corpID != "" || employee.staffID != "" {
		t.Fatalf("employee resolver must not run without organization identity: corp=%q staff=%q", employee.corpID, employee.staffID)
	}
}

func TestRobotTaskContextResolverContinuesWithoutIdentityAfterValidationError(t *testing.T) {
	validationErr := &orgemphsf.ValidationError{Field: "staff_id"}
	resolver := newRobotTaskContextResolver(&robotEmployeeResolverStub{err: validationErr})

	contextJSON, err := resolver.ResolveTaskContext(
		context.Background(),
		engine.ResolvedInstallation{},
		taskContextMessage(t, "ding-corp", "Staff-A_106201"),
	)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	assertDingTalkIdentityUnavailable(t, contextJSON, protocol.DingTalkRobotIdentityUnavailableLookupError)
}

func TestRobotTaskContextResolverContinuesWithoutIdentityAfterInfrastructureError(t *testing.T) {
	infraErr := errors.New("HSF unavailable")
	resolver := newRobotTaskContextResolver(&robotEmployeeResolverStub{err: infraErr})

	contextJSON, err := resolver.ResolveTaskContext(
		context.Background(),
		engine.ResolvedInstallation{},
		taskContextMessage(t, "ding-corp", "Staff-A_106201"),
	)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	assertDingTalkIdentityUnavailable(t, contextJSON, protocol.DingTalkRobotIdentityUnavailableLookupError)
}

func TestRobotTaskContextResolverBuildsIdentityForOpaqueStaffID(t *testing.T) {
	employee := &robotEmployeeResolverStub{employee: orgemphsf.Employee{
		UID:     "24710833",
		OrgID:   "439446171",
		StaffID: "Staff-A_106201",
	}}
	resolver := newRobotTaskContextResolver(employee)

	contextJSON, err := resolver.ResolveTaskContext(
		context.Background(),
		engine.ResolvedInstallation{},
		taskContextMessage(t, "ding-corp", "Staff-A_106201"),
	)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	if employee.corpID != "ding-corp" || employee.staffID != "Staff-A_106201" {
		t.Fatalf("resolver arguments = corpID %q staffID %q", employee.corpID, employee.staffID)
	}
	var payload map[string]protocol.DingTalkRobotIdentity
	if err := json.Unmarshal(contextJSON, &payload); err != nil {
		t.Fatalf("decode task context: %v", err)
	}
	identity := payload[protocol.DingTalkRobotIdentityJSONKey]
	if identity.UID != "24710833" || identity.OrgID != "439446171" {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestRobotTaskContextResolverCarriesStreamSourceWithIdentity(t *testing.T) {
	employee := &robotEmployeeResolverStub{employee: orgemphsf.Employee{
		UID:   "24710833",
		OrgID: "439446171",
	}}
	resolver := newRobotTaskContextResolver(employee)
	raw, err := json.Marshal(dingtalkRawEvent{
		SenderCorpID:  "ding-corp",
		SenderStaffID: "Staff-A_106201",
		StreamSource: &protocol.DingTalkStreamSource{
			Hostname:     "dt-fde-multica033008056137.pre.na620",
			NodeID:       "node-a",
			ConnectionID: "node-a-g3",
		},
	})
	if err != nil {
		t.Fatalf("marshal raw message: %v", err)
	}
	contextJSON, err := resolver.ResolveTaskContext(
		context.Background(),
		engine.ResolvedInstallation{},
		channel.InboundMessage{Raw: raw},
	)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(contextJSON, &payload); err != nil {
		t.Fatalf("decode task context: %v", err)
	}
	var source protocol.DingTalkStreamSource
	if err := json.Unmarshal(payload[protocol.DingTalkStreamSourceJSONKey], &source); err != nil {
		t.Fatalf("decode Stream source: %v", err)
	}
	if source.Hostname != "dt-fde-multica033008056137.pre.na620" || source.NodeID != "node-a" || source.ConnectionID != "node-a-g3" {
		t.Fatalf("Stream source = %+v", source)
	}
	if _, ok := payload[protocol.DingTalkRobotIdentityJSONKey]; !ok {
		t.Fatal("robot identity missing from combined task context")
	}
}

func assertDingTalkIdentityUnavailable(t *testing.T, contextJSON []byte, wantReason string) {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(contextJSON, &payload); err != nil {
		t.Fatalf("decode task context: %v", err)
	}
	var unavailable protocol.DingTalkRobotIdentityUnavailable
	if err := json.Unmarshal(payload[protocol.DingTalkRobotIdentityUnavailableJSONKey], &unavailable); err != nil {
		t.Fatalf("decode identity unavailable marker: %v", err)
	}
	if unavailable.Reason != wantReason {
		t.Fatalf("identity unavailable reason = %q, want %q", unavailable.Reason, wantReason)
	}
}
