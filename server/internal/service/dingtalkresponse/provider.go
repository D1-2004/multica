package dingtalkresponse

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
)

type ContextIssuer interface {
	CreateContext(context.Context, agentidentityhsf.CreateContextRequest) (agentidentityhsf.CreateContextResult, error)
}

// Provider authenticates independently for each call, without retaining tokens.
type Provider interface {
	Send(context.Context, ActionInput, string) (dwsclient.SendResult, error)
	Query(context.Context, ActionInput, string) (dwsclient.SendStatus, error)
}

type DWSConfig struct {
	AgentIdentity   ContextIssuer
	BaseURL         string
	BaseURLProvider func() string
	ClientSecret    string
	CLIPath         string
	HTTPClient      *http.Client
}

type dwsProvider struct {
	issuer ContextIssuer
	redeem dwsclient.Redeemer
	cli    dwsclient.CLI
}

func NewDWSProvider(cfg DWSConfig) Provider {
	return &dwsProvider{issuer: cfg.AgentIdentity,
		redeem: dwsclient.Redeemer{BaseURL: cfg.BaseURL, BaseURLProvider: cfg.BaseURLProvider, Client: cfg.HTTPClient, UserAgent: "dt-fde-multica/response-actions"},
		cli:    dwsclient.CLI{Path: cfg.CLIPath, ClientSecret: cfg.ClientSecret},
	}
}

// NotSubmittedError identifies errors before the provider send was invoked.
// Only this class is eligible for automatic retry with the same action key.
type NotSubmittedError struct{ Err error }

func (e *NotSubmittedError) Error() string { return "DWS response was not submitted" }
func (e *NotSubmittedError) Unwrap() error { return e.Err }

func (p *dwsProvider) Send(ctx context.Context, in ActionInput, key string) (dwsclient.SendResult, error) {
	dir, cleanup, err := p.authenticate(ctx, in)
	if err != nil {
		return dwsclient.SendResult{}, &NotSubmittedError{Err: err}
	}
	defer cleanup()
	req := dwsclient.SendRequest{Content: in.Text, ShowAITag: in.ShowAITag, IdempotencyKey: key}
	if in.IsGroup {
		req.ConversationID = in.ConversationID
		if in.SenderOpenDingTalkID != "" {
			req.AtOpenDingTalkID = in.SenderOpenDingTalkID
			mention := "<@" + in.SenderOpenDingTalkID + ">"
			if !strings.Contains(req.Content, mention) {
				req.Content = mention + " " + req.Content
			}
		}
	} else {
		req.RecipientOpenDingTalkID = in.SenderOpenDingTalkID
	}
	return p.cli.Send(ctx, dir, req)
}

func (p *dwsProvider) Query(ctx context.Context, in ActionInput, taskID string) (dwsclient.SendStatus, error) {
	dir, cleanup, err := p.authenticate(ctx, in)
	if err != nil {
		return dwsclient.SendStatus{}, err
	}
	defer cleanup()
	return p.cli.QuerySendStatus(ctx, dir, taskID)
}

func (p *dwsProvider) authenticate(ctx context.Context, in ActionInput) (string, func(), error) {
	if p == nil || p.issuer == nil {
		return "", nil, errors.New("DWS response provider is not configured")
	}
	runID := "response-dws-" + uuid.NewString()
	issued, err := p.issuer.CreateContext(ctx, agentidentityhsf.CreateContextRequest{
		RequestID: runID, TaskID: runID, AgentID: in.AgentID, RuntimeType: "SERVER", RuntimeID: runID,
		Reason: "Multica DingTalk response action",
		Source: map[string]string{"app": "dt-fde-multica", "identity_source": "response_action"},
		UID:    in.DWSUID, OrgID: in.DWSOrgID, TTLSeconds: 120,
	})
	if err != nil {
		return "", nil, fmt.Errorf("issue DWS response identity: %w", err)
	}
	credential, err := p.redeem.Redeem(ctx, issued.ContextToken)
	if err != nil {
		return "", nil, err
	}
	if credential.UID != in.DWSUID {
		return "", nil, errors.New("DWS response identity changed during redemption")
	}
	dir, err := os.MkdirTemp("", "multica-response-dws-")
	if err != nil {
		return "", nil, errors.New("create isolated DWS response directory")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.Chmod(dir, 0700); err != nil {
		cleanup()
		return "", nil, errors.New("secure isolated DWS response directory")
	}
	if err := p.cli.Exchange(ctx, dir, credential); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}
