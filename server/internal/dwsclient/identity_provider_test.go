package dwsclient

import (
	"context"
	"errors"
	"testing"
)

// versionProvider issues one identity another way, under a version.
type versionProvider struct {
	uid     string
	version string
	err     error
}

func (p versionProvider) Resolve(_ context.Context, _ Shared, id Identity, base IdentityMint) (Identity, func(context.Context) (Credential, error), error) {
	if p.err != nil {
		return id, nil, p.err
	}
	if id.UID != p.uid {
		return id, func(ctx context.Context) (Credential, error) { return base(ctx, id) }, nil
	}
	id.CredentialVersion = p.version
	return id, func(context.Context) (Credential, error) {
		return Credential{UID: id.UID, ClientID: "client-1", AuthCode: "other-way"}, nil
	}, nil
}

// Every shared session and mint goes through the registered provider: its
// mint issues the identities it chooses, under their own key, and its
// failures stop the session; the others keep the caller's mint.
func TestIdentityProviderChoosesHowIdentitiesAreIssued(t *testing.T) {
	f, _, _ := startSDK(t, func(string, map[string]any) string { return toolOK(`{"openTaskId":"task-1"}`) })
	t.Cleanup(func() { SetIdentityProvider(nil) })
	shared := Shared{CLI: CLI{ClientSecret: "secret", Environment: "staging"}}
	var base []string
	mint := func(_ context.Context, id Identity) (Credential, error) {
		base = append(base, id.UID)
		return Credential{UID: id.UID, ClientID: "client-1", AuthCode: "agent-identity"}, nil
	}
	linked := Identity{AgentID: "agent-1", UID: "42", OrgID: "org-1"}

	// Without a provider the caller's mint issues the identity.
	open := func(id Identity) {
		t.Helper()
		_, cleanup, ok, err := shared.Open(context.Background(), id, mint)
		if !ok || err != nil {
			t.Fatalf("open %+v: %v %v", id, ok, err)
		}
		cleanup()
	}
	open(linked)
	if len(base) != 1 {
		t.Fatalf("base mints = %v", base)
	}
	exchanges := len(f.tokens)

	// The provider issues the linked identity its own way, keyed apart from
	// the token the caller's mint left behind; another identity is untouched.
	SetIdentityProvider(versionProvider{uid: "42", version: "deap-1"})
	open(linked)
	if len(base) != 1 || len(f.tokens) != exchanges+1 {
		t.Fatalf("the provider's identity reused the usual token: base=%v exchanges=%d", base, len(f.tokens))
	}
	open(Identity{AgentID: "agent-1", UID: "43", OrgID: "org-1"})
	if len(base) != 2 || base[1] != "43" {
		t.Fatalf("an unlinked identity left the caller's mint: %v", base)
	}
	if c, err := shared.Mint(context.Background(), linked, mint); err != nil || c.AuthCode != "other-way" {
		t.Fatalf("CLI-transport mint = %+v %v", c, err)
	}

	// A provider failure stops the session instead of falling back.
	boom := errors.New("links unreadable")
	SetIdentityProvider(versionProvider{err: boom})
	if _, _, _, err := shared.Open(context.Background(), linked, mint); !errors.Is(err, boom) {
		t.Fatalf("provider failure = %v", err)
	}
	if _, err := shared.Mint(context.Background(), linked, mint); !errors.Is(err, boom) {
		t.Fatalf("provider failure on mint = %v", err)
	}
}
