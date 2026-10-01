// Package dwsidentity is the server's DWS identity provider
// (dwsclient.IdentityProvider): it decides how an execution identity's DWS
// credential is issued, for every server path that acts as the identity —
// native event streams, managed replies, user decisions, Coordinator
// history, scene memory and Router callback delivery (sandbox tasks redeem
// their own credentials and are not covered yet).
//
// Most identities are issued through Agent Identity: the caller's own mint.
// A DingTalk digital employee (DEAP) is not: its Agent Identity credential
// acts as another principal, which receives none of the employee's events
// and can neither read nor reply in its conversations. DEAP issues the
// employee's own DWS auth code, but only to the employee's supervisor. A
// DEAP link (set by deployment operators) names the employee and the
// supervisor; the supervisor's credential is issued through Agent Identity
// as usual, and asks DEAP for the employee's code.
package dwsidentity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dws"
)

// LinkStore reads DEAP links; the generated queries in production.
type LinkStore interface {
	GetDWSNativeDEAPLink(context.Context, db.GetDWSNativeDEAPLinkParams) (db.AgentDwsNativeDeapLink, error)
}

// Supervisor is a supervisor's DWS session as DEAP needs it. DEAP answers
// {success, data}: CallRaw keeps the whole payload (Call keeps only a result
// field).
type Supervisor interface {
	CallRaw(ctx context.Context, server dws.Server, tool string, args any) (json.RawMessage, error)
	Token() dws.Token
}

// Provider issues DEAP-linked identities through DEAP and every other
// identity through the caller's Agent Identity mint.
type Provider struct {
	Links LinkStore
	// OpenSupervisor opens the supervisor's session; nil opens it on the
	// shared session the identity is opened on (tests replace it).
	OpenSupervisor func(ctx context.Context, s dwsclient.Shared, id dwsclient.Identity, base dwsclient.IdentityMint) (Supervisor, error)
}

// Resolve implements dwsclient.IdentityProvider. A linked identity's
// credential carries the link's version, so a changed link mints afresh;
// the key and the mint come from the same read of the link.
func (p *Provider) Resolve(ctx context.Context, s dwsclient.Shared, id dwsclient.Identity, base dwsclient.IdentityMint) (dwsclient.Identity, func(context.Context) (dwsclient.Credential, error), error) {
	id.CredentialVersion = ""
	usual := func(ctx context.Context) (dwsclient.Credential, error) { return base(ctx, id) }
	agentID, err := util.ParseUUID(id.AgentID)
	if p == nil || p.Links == nil || err != nil {
		return id, usual, nil
	}
	link, err := p.Links.GetDWSNativeDEAPLink(ctx, db.GetDWSNativeDEAPLinkParams{AgentID: agentID, DwsUid: id.UID, OrgID: id.OrgID})
	if errors.Is(err, pgx.ErrNoRows) {
		return id, usual, nil
	}
	if err != nil {
		return id, nil, fmt.Errorf("load the identity's DEAP link: %w", err)
	}
	if link.SupervisorUid == id.UID {
		// The supervisor would resolve to this same link and wait on its
		// own token lock.
		return id, nil, errors.New("the DEAP link names the employee as its own supervisor")
	}
	id.CredentialVersion = Version(link)
	return id, func(ctx context.Context) (dwsclient.Credential, error) {
		return p.mintThroughDEAP(ctx, s, base, id, link)
	}, nil
}

// Version names a link without its ids (it ends up in Redis keys and event
// stream fingerprints).
func Version(link db.AgentDwsNativeDeapLink) string {
	sum := sha256.Sum256([]byte(link.DeapAgentUuid + "\x00" + link.SupervisorUid))
	return "deap-" + hex.EncodeToString(sum[:8])
}

func (p *Provider) mintThroughDEAP(ctx context.Context, s dwsclient.Shared, base dwsclient.IdentityMint,
	id dwsclient.Identity, link db.AgentDwsNativeDeapLink) (dwsclient.Credential, error) {
	open := p.OpenSupervisor
	if open == nil {
		open = func(ctx context.Context, s dwsclient.Shared, id dwsclient.Identity, base dwsclient.IdentityMint) (Supervisor, error) {
			return s.Client(ctx, id, base)
		}
	}
	// The supervisor is an ordinary identity: issued through Agent Identity.
	supervisor := dwsclient.Identity{AgentID: id.AgentID, UID: link.SupervisorUid, OrgID: id.OrgID}
	client, err := open(ctx, s, supervisor, base)
	if err != nil {
		return dwsclient.Credential{}, fmt.Errorf("open the DEAP supervisor's session: %w", err)
	}
	// The employee the link names must be this account: a link to another
	// employee would act as someone else.
	detail, err := call(ctx, client, "get_digital_employee_detail",
		map[string]any{"agentUuid": link.DeapAgentUuid, "snapshot": "published"})
	if err != nil {
		return dwsclient.Credential{}, fmt.Errorf("read the DEAP digital employee: %w", err)
	}
	profile, _ := detail["profile"].(map[string]any)
	corpID := scalar(profile["corpId"])
	if got := scalar(profile["userId"]); got != id.UID || corpID == "" {
		return dwsclient.Credential{}, errors.New("the DEAP digital employee is not this identity's account")
	}
	args := map[string]any{"agentUuid": link.DeapAgentUuid}
	// Ask for a code of this deployment's DWS app: it is exchanged with the
	// app's secret.
	if clientID := strings.TrimSpace(client.Token().ClientID); clientID != "" {
		args["clientId"] = clientID
	}
	data, err := call(ctx, client, "get_dws_auth_code", args)
	if err != nil {
		return dwsclient.Credential{}, fmt.Errorf("request the digital employee's DWS auth code: %w", err)
	}
	clientID, code := scalar(data["dwsClientId"]), scalar(data["dwsAuthCode"])
	if clientID == "" || code == "" {
		return dwsclient.Credential{}, errors.New("DEAP returned no DWS auth code")
	}
	slog.Info("DWS credential issued by DEAP", "event", "dws_deap_credential",
		"agent_id", id.AgentID, "deap_agent_uuid", link.DeapAgentUuid, "client_id", clientID)
	// The exchange checks the code is the employee's own (as the dws CLI's
	// managed exchange does).
	return dwsclient.Credential{UID: id.UID, ClientID: clientID, AuthCode: code,
		ExpectUserID: id.UID, ExpectCorpID: corpID}, nil
}

// call runs a DEAP tool as the supervisor and returns its business data; a
// business failure is an error.
func call(ctx context.Context, client Supervisor, tool string, args map[string]any) (map[string]any, error) {
	raw, err := client.CallRaw(ctx, dws.ServerDEAP, tool, args)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Success   *bool  `json:"success"`
		ErrorCode string `json:"errorCode"`
		ErrorMsg  string `json:"errorMsg"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if envelope.Success != nil && !*envelope.Success {
		return nil, fmt.Errorf("DEAP %s refused: %s %s", tool, clip(envelope.ErrorCode, 64), clip(envelope.ErrorMsg, 200))
	}
	data := businessData(raw)
	if data == nil {
		return nil, fmt.Errorf("DEAP %s returned no data", tool)
	}
	return data, nil
}

// businessData is a DEAP payload's business object: its data or result
// object, or the payload itself (as the dws CLI reads it).
func businessData(raw json.RawMessage) map[string]any {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	for _, key := range []string{"data", "result"} {
		if nested, ok := value[key].(map[string]any); ok {
			return nested
		}
	}
	return value
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", x), "0"), ".")
	}
	return ""
}

func clip(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit]) + "…"
}
