package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// A digital employee's sandbox gets the AuthCode its identity provider
// issues; an agent the provider does not own keeps Agent Identity's, and a
// failed issue stops the launch rather than falling back to it.
func TestIssuedDWSAuthCodeEnv(t *testing.T) {
	task := db.AgentTaskQueue{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, AgentID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}}
	bound := fcE2BDWSIdentity{UID: "858957531", OrgID: "439446171", Source: "agent_binding_fallback"}
	var asked []string
	issuer := func(_ context.Context, agentID, uid, orgID string) (string, string, bool, error) {
		asked = append(asked, agentID+"/"+uid+"/"+orgID)
		switch uid {
		case "858957531":
			return "ding-client", "issued-code", true, nil
		case "broken":
			return "", "", true, errors.New("DEAP refused")
		}
		return "", "", false, nil
	}
	l := &FCE2BLauncher{Config: FCE2BConfig{DWSClientSecret: "client-secret"}, DWSAuthCodeIssuer: issuer}

	env, err := l.issuedDWSAuthCodeEnv(context.Background(), task, bound)
	if err != nil || env[protocol.DWSAuthCodeEnvKey] != "issued-code" || env[protocol.DWSAuthCodeClientIDEnvKey] != "ding-client" || env["DWS_CLIENT_SECRET"] != "client-secret" {
		t.Fatalf("linked: %v %v", env, err)
	}
	if len(asked) != 1 || asked[0] != "02000000-0000-0000-0000-000000000000/858957531/439446171" {
		t.Fatalf("issuer asked %v", asked)
	}
	if env, err := l.issuedDWSAuthCodeEnv(context.Background(), task, fcE2BDWSIdentity{UID: "1", OrgID: "439446171"}); env != nil || err != nil {
		t.Fatalf("unlinked: %v %v", env, err)
	}
	if _, err := l.issuedDWSAuthCodeEnv(context.Background(), task, fcE2BDWSIdentity{UID: "broken", OrgID: "439446171"}); err == nil {
		t.Fatal("a failed issue fell back")
	}
	if env, err := l.issuedDWSAuthCodeEnv(context.Background(), task, fcE2BDWSIdentity{}); env != nil || err != nil {
		t.Fatalf("no bound identity: %v %v", env, err)
	}
	noSecret := &FCE2BLauncher{DWSAuthCodeIssuer: issuer}
	if _, err := noSecret.issuedDWSAuthCodeEnv(context.Background(), task, bound); err == nil {
		t.Fatal("an issued AuthCode was handed out without the client secret")
	}
	for _, key := range []string{protocol.DWSAuthCodeEnvKey, protocol.DWSAuthCodeClientIDEnvKey} {
		if !isAllowedFCE2BRunnerExtraEnv(key) {
			t.Fatalf("%s does not reach the runner", key)
		}
	}
}
