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

// cliFor is the CLI of one action: the provider's, pinned to the action's
// DWS environment when it names one.
func (p *dwsProvider) cliFor(in ActionInput) (dwsclient.CLI, error) {
	cli := p.cli
	switch in.DWSEnvironment {
	case "":
	case "production", "staging":
		cli.Environment, cli.MCPBaseURL = in.DWSEnvironment, ""
	default:
		return dwsclient.CLI{}, errors.New("unsupported DWS response environment")
	}
	return cli, nil
}

func (p *dwsProvider) Send(ctx context.Context, in ActionInput, key string) (dwsclient.SendResult, error) {
	if p == nil {
		return dwsclient.SendResult{}, &NotSubmittedError{Err: errors.New("DWS response provider is not configured")}
	}
	cli, err := p.cliFor(in)
	if err != nil {
		return dwsclient.SendResult{}, &NotSubmittedError{Err: err}
	}
	dir, cleanup, err := p.authenticateWith(ctx, cli, in)
	if err != nil {
		return dwsclient.SendResult{}, &NotSubmittedError{Err: err}
	}
	defer cleanup()
	if in.A2UICard != nil {
		if err := validateInput(in); err != nil {
			return dwsclient.SendResult{}, &NotSubmittedError{Err: err}
		}
		request := employeeQuestionSendTarget(in, key)
		receipt, err := cli.SendA2UI(ctx, dir, request)
		if err != nil {
			return dwsclient.SendResult{}, err
		}
		return dwsclient.SendResult{OpenTaskID: receipt.TaskID, OpenConversationID: receipt.ConversationID, OpenMessageID: receipt.MessageID, A2UIReceipt: &receipt}, nil
	}
	req := dwsclient.SendRequest{Content: in.Text, ShowAITag: in.ShowAITag, IdempotencyKey: key, ReplyToOpenMsgID: strings.TrimSpace(in.ReplyToOpenMsgID)}
	if in.IsGroup || req.ReplyToOpenMsgID != "" {
		req.ConversationID = in.ConversationID
		if in.IsGroup && in.SenderOpenDingTalkID != "" {
			req.AtOpenDingTalkID = in.SenderOpenDingTalkID
			// A quote reply is already addressed to this sender by DingTalk;
			// CLI.Send drops any addressing placeholder there. Only a plain
			// group send has to carry one.
			mention := dwsclient.MentionToken(in.SenderOpenDingTalkID)
			if req.ReplyToOpenMsgID == "" && !strings.Contains(req.Content, mention) {
				req.Content = mention + " " + req.Content
			}
		}
	} else {
		req.RecipientOpenDingTalkID = in.SenderOpenDingTalkID
	}
	return cli.Send(ctx, dir, req)
}

func (p *dwsProvider) Query(ctx context.Context, in ActionInput, taskID string) (dwsclient.SendStatus, error) {
	if p == nil {
		return dwsclient.SendStatus{}, errors.New("DWS response provider is not configured")
	}
	cli, err := p.cliFor(in)
	if err != nil {
		return dwsclient.SendStatus{}, err
	}
	dir, cleanup, err := p.authenticateWith(ctx, cli, in)
	if err != nil {
		return dwsclient.SendStatus{}, err
	}
	defer cleanup()
	return cli.QuerySendStatus(ctx, dir, taskID)
}

func (p *dwsProvider) UpdateQuestionCard(ctx context.Context, in ActionInput, bizID string, messages []string) error {
	if err := validateQuestionCardUpdate(in, bizID, messages); err != nil {
		return err
	}
	if p == nil {
		return errors.New("DWS response provider is not configured")
	}
	cli, err := p.cliFor(in)
	if err != nil {
		return err
	}
	dir, cleanup, err := p.authenticateWith(ctx, cli, in)
	if err != nil {
		return err
	}
	defer cleanup()
	return cli.UpdateA2UI(ctx, dir, bizID, "FINISH", messages, nil)
}

func (p *dwsProvider) authenticate(ctx context.Context, in ActionInput) (string, func(), error) {
	if p == nil {
		return "", nil, errors.New("DWS response provider is not configured")
	}
	return p.authenticateWith(ctx, p.cli, in)
}

// authenticateWith opens a directory authenticated as the action's sender
// on cli's DWS gateway.
func (p *dwsProvider) authenticateWith(ctx context.Context, cli dwsclient.CLI, in ActionInput) (string, func(), error) {
	if p == nil || p.issuer == nil {
		return "", nil, errors.New("DWS response provider is not configured")
	}
	mint := func(ctx context.Context, id dwsclient.Identity) (dwsclient.Credential, error) {
		as := in
		as.AgentID, as.DWSUID, as.DWSOrgID = id.AgentID, id.UID, id.OrgID
		return p.mint(ctx, as)
	}
	identity := dwsclient.Identity{AgentID: in.AgentID, UID: in.DWSUID, OrgID: in.DWSOrgID}
	shared := dwsclient.Shared{CLI: cli}
	// The SDK transport reuses the identity's shared token and mints only
	// without one; the dws CLI exchanges a credential per call.
	if dir, cleanup, ok, err := shared.Open(ctx, identity, mint); ok {
		return dir, cleanup, err
	}
	credential, err := shared.Mint(ctx, identity, mint)
	if err != nil {
		return "", nil, err
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
	if err := cli.Exchange(ctx, dir, credential); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

// mint issues an Agent Identity context for the action's sender and
// redeems it.
func (p *dwsProvider) mint(ctx context.Context, in ActionInput) (dwsclient.Credential, error) {
	if p == nil || p.issuer == nil {
		return dwsclient.Credential{}, errors.New("DWS response provider is not configured")
	}
	runID := "response-dws-" + uuid.NewString()
	issued, err := p.issuer.CreateContext(ctx, agentidentityhsf.CreateContextRequest{
		RequestID: runID, TaskID: runID, AgentID: in.AgentID, RuntimeType: "SERVER", RuntimeID: runID,
		Reason: "Multica DingTalk response action",
		Source: map[string]string{"app": "dt-fde-multica", "identity_source": "response_action"},
		UID:    in.DWSUID, OrgID: in.DWSOrgID, TTLSeconds: 120,
	})
	if err != nil {
		return dwsclient.Credential{}, fmt.Errorf("issue DWS response identity: %w", err)
	}
	credential, err := p.redeem.Redeem(ctx, issued.ContextToken)
	if err != nil {
		return dwsclient.Credential{}, err
	}
	if credential.UID != in.DWSUID {
		return dwsclient.Credential{}, errors.New("DWS response identity changed during redemption")
	}
	return credential, nil
}

// A DM targets the trusted source speaker in the employee's identity scope.
// A group targets the scene directory's conversation. Observer-relative DM ids
// must not be treated as group targets.
func employeeQuestionSendTarget(in ActionInput, key string) dwsclient.A2UISendRequest {
	request := dwsclient.A2UISendRequest{BizID: in.A2UICard.QuestionID, RequestID: key, Summary: in.Text, Messages: in.A2UICard.Messages}
	if in.IsGroup {
		request.ConversationID = in.ConversationID
	} else {
		request.ReceiverOpenDingTalkID = in.SenderOpenDingTalkID
	}
	return request
}
