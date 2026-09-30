package contextcap

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// directJob plants one inbound Coordinator job of conversation cid whose
// dispatch sender is (staffID, name), recorded under dispatchOrg, created
// age ago.
func (f storeFixture) directJob(t *testing.T, cid, staffID, name, dispatchOrg string, age time.Duration) {
	t.Helper()
	command, err := json.Marshal(map[string]any{
		"source": map[string]any{"platform": "dingtalk", "type": "digital_employee"},
		"event": map[string]any{"data": map[string]any{
			"conversation": map[string]any{"openConversationId": cid, "type": "single"},
			"sender":       map[string]any{"staffId": staffID, "displayName": name},
		}},
		"externalIdentity": map[string]any{"dws": map[string]any{"orgId": dispatchOrg}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.tx.Exec(context.Background(), `INSERT INTO inbound_coordinator_job
		(acceptance_id, workspace_id, agent_id, user_id, endpoint_namespace_id, idempotency_key, command, chat_session_id, user_message_id, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'completed', now() - make_interval(secs => $10::double precision))`,
		uuid.NewString(), f.workspaceID, f.agentID, uuid.NewString(), uuid.NewString(), uuid.NewString(), command,
		uuid.NewString(), uuid.NewString(), age.Seconds()); err != nil {
		t.Fatal(err)
	}
}

// directLink plants a personal link minted in 1:1 chat cid for staffID,
// redeemed by userID, and userID's person grant living ttl.
func (f storeFixture) directLink(t *testing.T, orgID, cid, staffID, title, userID string, ttl time.Duration) {
	t.Helper()
	ctx := context.Background()
	link, err := InsertLink(ctx, f.tx, Link{
		TokenHash: HashLinkToken(uuid.NewString()), WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopePerson,
		OrgID: orgID, ScopeKey: staffID, ScopeTitle: title, ExtraSceneKey: cid,
	}, LinkTTLPerson)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RedeemLink(ctx, f.tx, link.TokenHash, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tx.Exec(ctx, `INSERT INTO context_config_grant
		(user_id, workspace_id, agent_id, scope_type, org_id, scope_key, scope_title, source, expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'person', $4, $5, $6, 'agent_link', now() + make_interval(secs => $7::double precision))`,
		userID, f.workspaceID, f.agentID, orgID, staffID, title, ttl.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func TestDirectScenePersonSources(t *testing.T) {
	f := openStoreTx(t)
	ctx := context.Background()
	const (
		org       = "org-direct"
		linked    = "cidDirectLinked=="
		expired   = "cidDirectExpired=="
		jobbed    = "cidDirectJobbed=="
		otherOrg  = "cidDirectOtherOrg=="
		untouched = "cidDirectNothing=="
	)
	if _, _, err := DirectScenePerson(ctx, f.tx, f.workspaceID, f.agentID, org, "staff-1"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("non-cid key: %v", err)
	}
	staff, title, err := DirectScenePerson(ctx, f.tx, f.workspaceID, f.agentID, org, untouched)
	if err != nil || staff != "" || title != "" {
		t.Fatalf("unknown DM = %q %q %v", staff, title, err)
	}

	// A redeemed personal link minted in the DM with a live person grant.
	alice := uuid.NewString()
	f.directLink(t, org, linked, "staff-alice", "Alice", alice, time.Hour)
	staff, title, err = DirectScenePerson(ctx, f.tx, f.workspaceID, f.agentID, org, linked)
	if err != nil || staff != "staff-alice" || title != "Alice" {
		t.Fatalf("linked DM = %q %q %v", staff, title, err)
	}
	// Only under the link's org.
	if staff, _, err := DirectScenePerson(ctx, f.tx, f.workspaceID, f.agentID, "org-other", linked); err != nil || staff != "" {
		t.Fatalf("linked DM under another org = %q %v", staff, err)
	}
	// A link whose grant expired proves nothing any more.
	f.directLink(t, org, expired, "staff-gone", "Gone", uuid.NewString(), -time.Minute)
	if staff, _, err := DirectScenePerson(ctx, f.tx, f.workspaceID, f.agentID, org, expired); err != nil || staff != "" {
		t.Fatalf("DM of an expired grant = %q %v", staff, err)
	}

	// The newest Coordinator job naming a sender wins; an older one and a
	// job without a staffId do not.
	f.directJob(t, jobbed, "staff-old", "Old", org, 2*time.Hour)
	f.directJob(t, jobbed, "staff-bob", "Bob", "", time.Hour)
	f.directJob(t, jobbed, "", "No staff", org, time.Minute)
	staff, title, err = DirectScenePerson(ctx, f.tx, f.workspaceID, f.agentID, org, jobbed)
	if err != nil || staff != "staff-bob" || title != "Bob" {
		t.Fatalf("job DM = %q %q %v", staff, title, err)
	}
	// A job's sender beats a link, and the link's title only fills a
	// missing name of the same person.
	f.directJob(t, linked, "staff-alice", "", org, time.Minute)
	staff, title, err = DirectScenePerson(ctx, f.tx, f.workspaceID, f.agentID, org, linked)
	if err != nil || staff != "staff-alice" || title != "Alice" {
		t.Fatalf("linked DM with an unnamed job = %q %q %v", staff, title, err)
	}
	// Jobs recorded under another agent org are skipped.
	f.directJob(t, otherOrg, "staff-foreign", "Foreign", "org-other", time.Minute)
	if staff, _, err := DirectScenePerson(ctx, f.tx, f.workspaceID, f.agentID, org, otherOrg); err != nil || staff != "" {
		t.Fatalf("DM of another org's job = %q %v", staff, err)
	}
}

func TestNormalizeScopeMCPConfig(t *testing.T) {
	for _, empty := range []string{"", "null", " null ", "{}", " { } "} {
		got, err := NormalizeScopeMCPConfig(json.RawMessage(empty))
		if err != nil || got != nil {
			t.Errorf("NormalizeScopeMCPConfig(%q) = %s %v, want nil", empty, got, err)
		}
	}
	for _, bad := range []string{"[]", `"x"`, "1", "true", "{", `{"a":}`, `{"mcpServers":` + strings.Repeat(" ", MaxScopeMCPConfigBytes) + `{}}`} {
		if _, err := NormalizeScopeMCPConfig(json.RawMessage(bad)); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("NormalizeScopeMCPConfig(%q) err=%v, want ErrInvalidInput", bad[:min(len(bad), 16)], err)
		}
	}
	got, err := NormalizeScopeMCPConfig(json.RawMessage(` { "mcpServers" : { "docs" : { "url" : "https://docs.example.test/mcp" } } } `))
	if err != nil || string(got) != `{"mcpServers":{"docs":{"url":"https://docs.example.test/mcp"}}}` {
		t.Fatalf("compacted = %s %v", got, err)
	}
}

func TestScopeMCPConfigStore(t *testing.T) {
	f := openStoreTx(t)
	ctx := context.Background()
	var migrated bool
	if err := f.tx.QueryRow(ctx, `SELECT to_regclass('context_scope_mcp_config') IS NOT NULL`).Scan(&migrated); err != nil || !migrated {
		t.Skip("context_scope_mcp_config is not migrated")
	}
	const org, cid = "org-mcp", "cidScopeMCP=="
	if _, err := GetScopeMCPConfig(ctx, f.tx, f.workspaceID, f.agentID, ScopeScene, org, cid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty scope: %v", err)
	}
	for name, in := range map[string]ScopeMCPConfigWrite{
		"offer scope":  {ScopeType: ScopeOffer, ScopeKey: cid},
		"non-cid key":  {ScopeType: ScopeScene, ScopeKey: "group-1"},
		"not object":   {ScopeType: ScopeScene, ScopeKey: cid, MCPConfig: json.RawMessage(`[1]`)},
		"bad actor id": {ScopeType: ScopePerson, ScopeKey: "staff-1", MCPConfig: json.RawMessage(`{"a":1}`), ActorID: "nope"},
	} {
		in.WorkspaceID, in.AgentID, in.OrgID = f.workspaceID, f.agentID, org
		if _, err := PutScopeMCPConfig(ctx, f.tx, in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err=%v", name, err)
		}
	}

	write := ScopeMCPConfigWrite{
		WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopeScene, OrgID: org, ScopeKey: cid,
		MCPConfig: json.RawMessage(`{"mcpServers": {"docs": {"url": "https://docs.example.test/mcp"}}}`),
	}
	stored, err := PutScopeMCPConfig(ctx, f.tx, write)
	if err != nil || string(stored.MCPConfig) != `{"mcpServers": {"docs": {"url": "https://docs.example.test/mcp"}}}` || stored.ScopeKey != cid {
		t.Fatalf("stored = %+v (%s) %v", stored, stored.MCPConfig, err)
	}
	write.MCPConfig = json.RawMessage(`{"mcpServers":{}}`)
	if _, err := PutScopeMCPConfig(ctx, f.tx, write); err != nil {
		t.Fatal(err)
	}
	got, err := GetScopeMCPConfig(ctx, f.tx, f.workspaceID, f.agentID, ScopeScene, org, cid)
	if err != nil || string(got.MCPConfig) != `{"mcpServers": {}}` {
		t.Fatalf("after update = %s %v", got.MCPConfig, err)
	}
	// Scopes are separate: the person scope and another org see nothing.
	if _, err := GetScopeMCPConfig(ctx, f.tx, f.workspaceID, f.agentID, ScopeScene, "org-other", cid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org: %v", err)
	}
	// A row of another workspace is never overwritten.
	other := write
	other.WorkspaceID = uuid.NewString()
	if _, err := PutScopeMCPConfig(ctx, f.tx, other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another workspace's write: %v", err)
	}
	// null clears the scope and deletes the row.
	write.MCPConfig = json.RawMessage(`null`)
	cleared, err := PutScopeMCPConfig(ctx, f.tx, write)
	if err != nil || cleared.MCPConfig != nil {
		t.Fatalf("cleared = %+v %v", cleared, err)
	}
	if _, err := GetScopeMCPConfig(ctx, f.tx, f.workspaceID, f.agentID, ScopeScene, org, cid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after clear: %v", err)
	}
}
