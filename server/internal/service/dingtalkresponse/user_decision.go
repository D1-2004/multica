package dingtalkresponse

import (
	"context"
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
	corp, err := s.cli.VerifyInternalGroup(ctx, s.dir, cid)
	if err != nil {
		return "", "", err
	}
	actor, err := s.cli.ResolveMessageSender(ctx, s.dir, cid, messageID)
	return corp, actor, err
}
func (s *decisionSession) Send(ctx context.Context, r userdecision.Request) (string, error) {
	receipt, err := s.cli.SendA2UI(ctx, s.dir, dwsclient.A2UISendRequest{ConversationID: r.ConversationID, BizID: r.CardID, RequestID: r.SendRequestID, Summary: r.Proposal.Question, Messages: userdecision.Card(r.ID, r.Proposal), Annotations: decisionAnnotation(r.ID, "question")})
	return receipt.BizID, err
}
func (s *decisionSession) Update(ctx context.Context, r userdecision.Request, status, text string) error {
	messages := userdecision.StatusCard(r.ID, text)
	componentID := "status"
	if r.State == "waiting" {
		// Refresh components only: keep the frozen choices and the client form data.
		messages = userdecision.WaitingCardUpdate(r.ID, r.Proposal)
		status = "INPUTTING"
		componentID = "question"
	}
	return s.cli.UpdateA2UI(ctx, s.dir, r.CardID, status, messages, decisionAnnotation(r.ID, componentID))
}
func (s *decisionSession) Consume(ctx context.Context, ready func(), consume func([]byte) error) error {
	return s.cli.ConsumeCardEvents(ctx, s.dir, ready, consume)
}

func (s *decisionSession) Reconcile(ctx context.Context, r userdecision.Request) error {
	return s.cli.UpdateA2UI(ctx, s.dir, r.CardID, "INPUTTING", userdecision.Card(r.ID, r.Proposal), decisionAnnotation(r.ID, "question"))
}

func decisionAnnotation(surfaceID, componentID string) []dwsclient.A2UIAnnotation {
	return []dwsclient.A2UIAnnotation{{SurfaceID: surfaceID, ComponentID: componentID, Type: "artifact"}}
}
