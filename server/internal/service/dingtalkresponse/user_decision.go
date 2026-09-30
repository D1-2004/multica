package dingtalkresponse

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/service/userdecision"
	"strings"
	"time"
)

type decisionTransport struct {
	provider *dwsProvider
	events   EventConnections
}
type decisionSession struct {
	cli      dwsclient.CLI
	dir      string
	close    func()
	identity dwsclient.Identity
	events   EventConnections
}

// EventConnections is the server's shared DWS event connections
// (dwseventsource): with the SDK transport a decision session waits for its
// identity's connection, on whichever replica holds it, instead of opening
// its own; the connection persists card events through the decision
// service.
type EventConnections interface {
	// Active reports whether the shared connections are switched on.
	Active() bool
	Ready(context.Context, dwsclient.Identity) bool
	Kick()
}

// SetDecisionEventConnections hands the transport the shared connections.
func SetDecisionEventConnections(t userdecision.Transport, events EventConnections) {
	if dt, ok := t.(*decisionTransport); ok {
		dt.events = events
	}
}

// DecisionSessions returns what a shared event connection needs to act as
// a decision sender: the transport's DWS configuration and its identity
// mint (an Agent Identity context, redeemed).
func DecisionSessions(t userdecision.Transport) (dwsclient.Shared, func(context.Context, dwsclient.Identity) (dwsclient.Credential, error), bool) {
	dt, ok := t.(*decisionTransport)
	if !ok {
		return dwsclient.Shared{}, nil, false
	}
	return dwsclient.Shared{CLI: dt.provider.cli}, func(ctx context.Context, id dwsclient.Identity) (dwsclient.Credential, error) {
		return dt.provider.mint(ctx, ActionInput{AgentID: id.AgentID, DWSUID: id.UID, DWSOrgID: id.OrgID})
	}, true
}

func NewDecisionTransport(cfg DWSConfig, mcpURL string) userdecision.Transport {
	p := NewDWSProvider(cfg).(*dwsProvider)
	p.cli.MCPBaseURL = mcpURL
	p.cli.Environment = "production"
	if strings.Contains(mcpURL, "pre-mcp.") {
		p.cli.Environment = "staging"
	}
	return &decisionTransport{provider: p}
}
func (t *decisionTransport) Open(ctx context.Context, r userdecision.Request) (userdecision.Session, error) {
	dir, cleanup, err := t.provider.authenticate(ctx, ActionInput{AgentID: r.AgentID, DWSUID: r.SenderUID, DWSOrgID: r.SenderOrgID})
	if err != nil {
		return nil, err
	}
	return &decisionSession{cli: t.provider.cli, dir: dir, close: cleanup, events: t.events,
		identity: dwsclient.Identity{AgentID: r.AgentID, UID: r.SenderUID, OrgID: r.SenderOrgID}}, nil
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
	if s.events != nil && dwsclient.SDKSession(s.dir) {
		return awaitEventConnection(ctx, s.events, s.identity, ready)
	}
	if s.events == nil {
		return s.cli.ConsumeCardEvents(ctx, s.dir, ready, consume)
	}
	// Switched on while this consumer runs on the dws CLI: end it, so the
	// decision service reopens the identity on the shared connection
	// instead of keeping a second stream until the consumer's cycle ends.
	// The switch also selects the SDK transport, so the reopened session
	// never lands back here.
	cliCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	switched := make(chan struct{})
	go func() {
		tick := time.NewTicker(switchCheckInterval)
		defer tick.Stop()
		for {
			select {
			case <-cliCtx.Done():
				return
			case <-tick.C:
				if s.events.Active() {
					// Start the shared stream now, not at its next sweep.
					s.events.Kick()
					close(switched)
					cancel()
					return
				}
			}
		}
	}()
	err := s.cli.ConsumeCardEvents(cliCtx, s.dir, ready, consume)
	select {
	case <-switched:
		return errors.New("DWS event connections switched on")
	default:
		return err
	}
}

// switchCheckInterval is how often a dws CLI consumer checks whether the
// shared event connections were switched on.
var switchCheckInterval = time.Second

// eventConnectionLost is how long a connection may stay gone after it was
// ready before the consumer reports it lost (the service then re-opens).
const eventConnectionLost = 45 * time.Second

// awaitEventConnection stands in for a session's own consumer: it reports
// ready once the identity's shared connection is connected, and returns
// when ctx ends or the connection stays gone.
func awaitEventConnection(ctx context.Context, events EventConnections, id dwsclient.Identity, ready func()) error {
	events.Kick()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	fired := false
	var lastSeen time.Time
	for {
		if !events.Active() {
			// Switched off: end now, so the decision service reopens the
			// identity on the dws CLI instead of waiting on a stream that
			// is being torn down.
			return errors.New("DWS event connections are switched off")
		}
		if events.Ready(ctx, id) {
			if !fired {
				fired = true
				ready()
			}
			lastSeen = time.Now()
		} else if fired && time.Since(lastSeen) > eventConnectionLost {
			return errors.New("DWS event connection lost")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
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
