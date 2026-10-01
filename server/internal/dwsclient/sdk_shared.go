package dwsclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// Identity is the DingTalk account a server operation acts as: an agent's
// bound DWS user in one organization.
type Identity struct {
	AgentID string
	UID     string
	OrgID   string
	// CredentialVersion names how the identity's credential is issued when
	// that is not the usual Agent Identity (set by the IdentityProvider); a
	// new version is minted afresh instead of reusing the shared token.
	CredentialVersion string
}

// IdentityMint issues id's credential through Agent Identity (a context and
// its redeem); callers pass theirs, for any identity they name.
type IdentityMint func(ctx context.Context, id Identity) (Credential, error)

// IdentityProvider chooses how an execution identity's DWS credential is
// issued. Usually that is base (Agent Identity); a provider may issue some
// identities another way (a DingTalk digital employee through DEAP and its
// supervisor). It returns the identity to key the credential by, carrying a
// CredentialVersion that names any other way, and the mint to use.
type IdentityProvider interface {
	Resolve(ctx context.Context, s Shared, id Identity, base IdentityMint) (Identity, func(context.Context) (Credential, error), error)
}

var identityProvider atomic.Pointer[IdentityProvider]

// SetIdentityProvider installs the provider every shared session and mint
// consults; nil issues every identity through Agent Identity.
func SetIdentityProvider(p IdentityProvider) {
	if p == nil {
		identityProvider.Store(nil)
		return
	}
	identityProvider.Store(&p)
}

// resolve is how id's credential is keyed and minted.
func (s Shared) resolve(ctx context.Context, id Identity, base IdentityMint) (Identity, func(context.Context) (Credential, error), error) {
	if p := identityProvider.Load(); p != nil {
		return (*p).Resolve(ctx, s, id, base)
	}
	id.CredentialVersion = ""
	return id, func(ctx context.Context) (Credential, error) { return base(ctx, id) }, nil
}

// Issue returns the AuthCode an identity provider issues for id, for a
// caller that hands it to another process to exchange (a task sandbox). ok is
// false when no provider owns id: the caller keeps its own credential path.
func (s Shared) Issue(ctx context.Context, id Identity, base IdentityMint) (Credential, bool, error) {
	resolved, mint, err := s.resolve(ctx, id, base)
	if err != nil {
		return Credential{}, false, err
	}
	if resolved.CredentialVersion == "" {
		return Credential{}, false, nil
	}
	credential, err := mint(ctx)
	if err != nil {
		return Credential{}, true, err
	}
	if credential.UID != resolved.UID {
		return Credential{}, true, errors.New("DWS identity changed during redemption")
	}
	return credential, true, nil
}

// Mint issues id's credential the way the provider chooses, for the dws CLI
// transport, which exchanges a credential per call.
func (s Shared) Mint(ctx context.Context, id Identity, base IdentityMint) (Credential, error) {
	resolved, mint, err := s.resolve(ctx, id, base)
	if err != nil {
		return Credential{}, err
	}
	credential, err := mint(ctx)
	if err == nil && credential.UID != resolved.UID {
		err = errors.New("DWS identity changed during redemption")
	}
	return credential, err
}

// Shared opens directories on identities' shared SDK clients. With the SDK
// transport an identity is exchanged once and its token is reused by every
// operation, and through the token store by every replica; the mint (as the
// IdentityProvider chooses) runs only when no usable token exists.
type Shared struct {
	CLI CLI
}

var tokenStore atomic.Pointer[dws.TokenStore]

// SetTokenStore installs the store replicas share tokens through (Redis,
// sealed); nil keeps tokens per process.
func SetTokenStore(store dws.TokenStore) {
	if store == nil {
		tokenStore.Store(nil)
		return
	}
	tokenStore.Store(&store)
}

func sharedTokenStore() dws.TokenStore {
	if s := tokenStore.Load(); s != nil {
		return *s
	}
	return nil
}

// pools holds one dws.Pool per endpoint and app secret.
var pools sync.Map

func (s Shared) pool(mcp, gateway string) *dws.Pool {
	secret := sha256.Sum256([]byte(strings.TrimSpace(s.CLI.ClientSecret)))
	// The ticket mode is part of the pool's Config, so pools differ by it.
	poolKey := mcp + "\x00" + gateway + "\x00" + hex.EncodeToString(secret[:8]) + "\x00" + s.CLI.StreamTicketMode
	if p, ok := pools.Load(poolKey); ok {
		return p.(*dws.Pool)
	}
	p, _ := pools.LoadOrStore(poolKey, &dws.Pool{Config: s.CLI.sdkConfig(mcp, gateway), Store: sharedTokenStore()})
	return p.(*dws.Pool)
}

// Open returns a directory authenticated as id, for the operations of
// this package, and its cleanup. ok is false when the SDK transport is not
// selected: the caller then exchanges a credential per call with the dws
// CLI, as before.
func (s Shared) Open(ctx context.Context, id Identity, base IdentityMint) (dir string, cleanup func(), ok bool, err error) {
	if !sdkSelected() {
		return "", nil, false, nil
	}
	if strings.TrimSpace(s.CLI.ClientSecret) == "" {
		return "", nil, true, errors.New("DWS client secret is not configured")
	}
	if id.AgentID == "" || id.UID == "" || id.OrgID == "" {
		return "", nil, true, errors.New("DWS identity is incomplete")
	}
	pruneSDKClients()
	mcp, gateway, err := s.CLI.sdkEndpoints()
	if err != nil {
		return "", nil, true, err
	}
	id, mint, err := s.resolve(ctx, id, base)
	if err != nil {
		return "", nil, true, err
	}
	identityKey := s.identityKey(mcp, id)
	started := time.Now()
	var mintErr error
	minted := false
	client, err := s.pool(mcp, gateway).ClientWith(ctx, identityKey, func(ctx context.Context) (dws.AuthCode, error) {
		minted = true
		credential, err := mint(ctx)
		if err == nil && credential.UID != id.UID {
			err = errors.New("DWS identity changed during redemption")
		}
		if err != nil {
			mintErr = err
			return dws.AuthCode{}, err
		}
		return dws.AuthCode{Code: credential.AuthCode, ClientID: credential.ClientID,
			ExpectUserID: credential.ExpectUserID, ExpectCorpID: credential.ExpectCorpID}, nil
	})
	if err != nil {
		if mintErr != nil {
			// The caller's own failure (identity context or redeem).
			return "", nil, true, mintErr
		}
		// Other failures stay bounded, like the CLI exchange's.
		return "", nil, true, commandFailed(ctx, "DWS AuthCode exchange failed", err)
	}
	// minted=false means the identity's shared token served: no Agent
	// Identity context and no exchange for this operation.
	slog.Info("DWS SDK session opened", "event", "dws_sdk_session", "agent_id", id.AgentID,
		"minted", minted, "elapsed_ms", time.Since(started).Milliseconds())
	dir, err = os.MkdirTemp("", "multica-dws-shared-")
	if err != nil {
		return "", nil, true, errors.New("create isolated DWS directory")
	}
	cleanup = func() {
		sdkClients.Delete(filepath.Clean(dir))
		_ = os.RemoveAll(dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		cleanup()
		return "", nil, true, errors.New("secure isolated DWS directory")
	}
	if err := writeSDKSession(dir, sdkSession{MCP: mcp, Gateway: gateway, UID: id.UID, CorpID: client.Token().CorpID}); err != nil {
		cleanup()
		return "", nil, true, err
	}
	sdkClients.Store(filepath.Clean(dir), client)
	return dir, cleanup, true, nil
}

// identityKey names id's credentials in the pool and the shared token store.
// The usual Agent Identity credential keeps the key it always had.
func (s Shared) identityKey(mcp string, id Identity) string {
	key := mcp + "\x00" + id.AgentID + "\x00" + id.UID + "\x00" + id.OrgID
	if version := strings.TrimSpace(id.CredentialVersion); version != "" {
		key += "\x00" + version
	}
	return key
}

// Client returns id's shared SDK client, for long-lived work such as an
// event connection; mint runs only when no usable token exists. It does
// not depend on the switch: callers that hold a connection decide that.
func (s Shared) Client(ctx context.Context, id Identity, base IdentityMint) (*dws.Client, error) {
	if strings.TrimSpace(s.CLI.ClientSecret) == "" {
		return nil, errors.New("DWS client secret is not configured")
	}
	if id.AgentID == "" || id.UID == "" || id.OrgID == "" {
		return nil, errors.New("DWS identity is incomplete")
	}
	mcp, gateway, err := s.CLI.sdkEndpoints()
	if err != nil {
		return nil, err
	}
	id, mint, err := s.resolve(ctx, id, base)
	if err != nil {
		return nil, err
	}
	var mintErr error
	client, err := s.pool(mcp, gateway).ClientWith(ctx, s.identityKey(mcp, id),
		func(ctx context.Context) (dws.AuthCode, error) {
			credential, err := mint(ctx)
			if err == nil && credential.UID != id.UID {
				err = errors.New("DWS identity changed during redemption")
			}
			if err != nil {
				mintErr = err
				return dws.AuthCode{}, err
			}
			return dws.AuthCode{Code: credential.AuthCode, ClientID: credential.ClientID,
				ExpectUserID: credential.ExpectUserID, ExpectCorpID: credential.ExpectCorpID}, nil
		})
	if err != nil {
		if mintErr != nil {
			return nil, mintErr
		}
		return nil, commandFailed(ctx, "DWS AuthCode exchange failed", err)
	}
	return client, nil
}
