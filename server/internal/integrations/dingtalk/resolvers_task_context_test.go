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

func TestRobotTaskContextResolverRejectsMissingOrganizationIdentity(t *testing.T) {
	resolver := newRobotTaskContextResolver(&robotEmployeeResolverStub{})
	_, err := resolver.ResolveTaskContext(context.Background(), engine.ResolvedInstallation{}, taskContextMessage(t, "", "Staff-A_106201"))
	if !errors.Is(err, engine.ErrTaskContextRejected) {
		t.Fatalf("error = %v, want ErrTaskContextRejected", err)
	}
}

func TestRobotTaskContextResolverRejectsValidationErrorWithoutMaskingIt(t *testing.T) {
	validationErr := &orgemphsf.ValidationError{Field: "staff_id"}
	resolver := newRobotTaskContextResolver(&robotEmployeeResolverStub{err: validationErr})

	_, err := resolver.ResolveTaskContext(
		context.Background(),
		engine.ResolvedInstallation{},
		taskContextMessage(t, "ding-corp", "Staff-A_106201"),
	)
	if !errors.Is(err, engine.ErrTaskContextRejected) {
		t.Fatalf("error = %v, want ErrTaskContextRejected", err)
	}
	var gotValidation *orgemphsf.ValidationError
	if !errors.As(err, &gotValidation) || gotValidation.Field != "staff_id" {
		t.Fatalf("error = %v, want wrapped staff_id ValidationError", err)
	}
}

func TestRobotTaskContextResolverPreservesInfrastructureError(t *testing.T) {
	infraErr := errors.New("HSF unavailable")
	resolver := newRobotTaskContextResolver(&robotEmployeeResolverStub{err: infraErr})

	_, err := resolver.ResolveTaskContext(
		context.Background(),
		engine.ResolvedInstallation{},
		taskContextMessage(t, "ding-corp", "Staff-A_106201"),
	)
	if errors.Is(err, engine.ErrTaskContextRejected) {
		t.Fatalf("infrastructure error was misclassified: %v", err)
	}
	if !errors.Is(err, infraErr) {
		t.Fatalf("error = %v, want wrapped infrastructure error", err)
	}
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
