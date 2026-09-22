package dingtalkresponse

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/service/userdecision"
	"strings"
)

type decisionTransport struct{ provider *dwsProvider }
type decisionSession struct {
	cli   dwsclient.CLI
	dir   string
	close func()
}

func NewDecisionTransport(cfg DWSConfig, mcpURL string) userdecision.Transport {
	p := NewDWSProvider(cfg).(*dwsProvider)
	p.cli.MCPBaseURL = mcpURL
	p.cli.Environment = "production"
	if strings.Contains(mcpURL, "pre-mcp.") {
		p.cli.Environment = "staging"
	}
	return &decisionTransport{p}
}
func (t *decisionTransport) Open(ctx context.Context, r userdecision.Request) (userdecision.Session, error) {
	dir, cleanup, err := t.provider.authenticate(ctx, ActionInput{AgentID: r.AgentID, DWSUID: r.SenderUID, DWSOrgID: r.SenderOrgID})
	if err != nil {
		return nil, err
	}
	return &decisionSession{t.provider.cli, dir, cleanup}, nil
}
func (s *decisionSession) Close() { s.close() }
func (s *decisionSession) Verify(ctx context.Context, cid, messageID string) (string, string, error) {
	corp, err := s.cli.DecisionOrganization(ctx, s.dir)
	if err != nil {
		return "", "", err
	}
	actor, err := s.cli.ResolveMessageSender(ctx, s.dir, cid, messageID)
	if err != nil {
		return "", "", &dwsclient.DecisionIdentityError{Code: "user_decision_initiator_lookup_failed"}
	}
	return corp, actor, nil
}
func (s *decisionSession) Send(ctx context.Context, r userdecision.Request) (string, error) {
	in, err := decisionCardRequest(r)
	if err != nil {
		return "", err
	}
	receipt, err := s.cli.SendA2UI(ctx, s.dir, in)
	return receipt.BizID, err
}
func (s *decisionSession) Update(ctx context.Context, r userdecision.Request, status, text string) error {
	messages := userdecision.StatusCard(r.ID, text)
	componentID := "status"
	if r.State == "waiting" {
		// Refresh components only: keep the frozen choices and the client form data.
		messages = userdecision.WaitingCardUpdate(r.ID, r.Proposal)
		status = "CONFIRMING"
		componentID = "question"
	}
	return s.cli.UpdateA2UI(ctx, s.dir, r.CardID, status, messages, decisionAnnotation(r.ID, componentID))
}
func (s *decisionSession) Consume(ctx context.Context, ready func(), consume func([]byte) error) error {
	return s.cli.ConsumeCardEvents(ctx, s.dir, ready, consume)
}

func (s *decisionSession) Reconcile(ctx context.Context, r userdecision.Request) error {
	return s.cli.UpdateA2UI(ctx, s.dir, r.CardID, "CONFIRMING", userdecision.Card(r.ID, r.Proposal), decisionAnnotation(r.ID, "question"))
}

func decisionAnnotation(surfaceID, componentID string) []dwsclient.A2UIAnnotation {
	return []dwsclient.A2UIAnnotation{{SurfaceID: surfaceID, ComponentID: componentID, Type: "artifact"}}
}

func decisionCardRequest(r userdecision.Request) (dwsclient.A2UISendRequest, error) {
	var snapshot struct {
		Turn struct{ ChatType string } `json:"turn"`
	}
	if json.Unmarshal(r.Snapshot, &snapshot) != nil {
		return dwsclient.A2UISendRequest{}, errors.New("invalid decision transport snapshot")
	}
	in := dwsclient.A2UISendRequest{BizID: r.CardID, RequestID: r.SendRequestID, Summary: r.Proposal.Question, Messages: userdecision.Card(r.ID, r.Proposal)}
	// The Host-frozen channel type selects the DWS target shape, not channel eligibility.
	switch snapshot.Turn.ChatType {
	case "p2p":
		if r.InitiatorID == "" {
			return in, errors.New("missing decision recipient")
		}
		in.ReceiverOpenDingTalkID = r.InitiatorID
	case "group":
		in.ConversationID = r.ConversationID
	default:
		return in, errors.New("missing decision channel type")
	}
	return in, nil
}
