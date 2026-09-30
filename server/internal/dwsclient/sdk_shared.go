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
}

// Shared opens directories on identities' shared SDK clients. With the SDK
// transport an identity is exchanged once and its token is reused by every
// operation, and through the token store by every replica; mint (an Agent
// Identity context and redeem) runs only when no usable token exists.
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
	poolKey := mcp + "\x00" + gateway + "\x00" + hex.EncodeToString(secret[:8])
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
func (s Shared) Open(ctx context.Context, id Identity, mint func(context.Context) (Credential, error)) (dir string, cleanup func(), ok bool, err error) {
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
	identityKey := mcp + "\x00" + id.AgentID + "\x00" + id.UID + "\x00" + id.OrgID
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
		return dws.AuthCode{Code: credential.AuthCode, ClientID: credential.ClientID}, nil
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

// Client returns id's shared SDK client, for long-lived work such as an
// event connection; mint runs only when no usable token exists. It does
// not depend on the switch: callers that hold a connection decide that.
func (s Shared) Client(ctx context.Context, id Identity, mint func(context.Context) (Credential, error)) (*dws.Client, error) {
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
	var mintErr error
	client, err := s.pool(mcp, gateway).ClientWith(ctx, mcp+"\x00"+id.AgentID+"\x00"+id.UID+"\x00"+id.OrgID,
		func(ctx context.Context) (dws.AuthCode, error) {
			credential, err := mint(ctx)
			if err == nil && credential.UID != id.UID {
				err = errors.New("DWS identity changed during redemption")
			}
			if err != nil {
				mintErr = err
				return dws.AuthCode{}, err
			}
			return dws.AuthCode{Code: credential.AuthCode, ClientID: credential.ClientID}, nil
		})
	if err != nil {
		if mintErr != nil {
			return nil, mintErr
		}
		return nil, commandFailed(ctx, "DWS AuthCode exchange failed", err)
	}
	return client, nil
}
