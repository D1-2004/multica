package dwsclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// runtime.use_dws_for_tag sends the server's DingTalk calls through the
// in-process DWS gateway SDK (pkg/dwsrpc) instead of spawning dws.
//
// The CLI contract is kept: Exchange runs in a fresh isolated directory and
// every later call on that directory uses what Exchange left there. The
// transport is therefore chosen once per directory, at Exchange, and a live
// switch never splits one operation between the two. Each SDK call returns
// what the dws CLI prints, so parsers and callers stay as they are.
//
// This is the server's transport only: cmd/server installs the switch, and
// nothing else does. The daemon and the sandbox's dws shim keep the dws CLI;
// sandbox-side shortcuts ship as the dws-shortcuts skill from dws-for-tag
// and are iterated by publishing that skill, never through this package or
// the sandbox image.

var sdkSelector atomic.Pointer[func() bool]

// SetSDKSelector installs the live switch; nil keeps the CLI.
func SetSDKSelector(selected func() bool) {
	if selected == nil {
		sdkSelector.Store(nil)
		return
	}
	sdkSelector.Store(&selected)
}

func sdkSelected() bool {
	selected := sdkSelector.Load()
	return selected != nil && (*selected)()
}

// sdkSessionFile marks a directory exchanged through the SDK. It holds no
// credential: the token lives only in the directory's Client (sdkClients),
// because every call on the directory must share one Client — the refresh
// token rotates, and a second Client refreshing on its own would spend it
// under the first.
const sdkSessionFile = "dws-sdk-session.json"

type sdkSession struct {
	// MCP is the DWS host (event control plane, DWS-hosted OAuth); Gateway
	// is its tool gateway.
	MCP     string `json:"mcp"`
	Gateway string `json:"gateway"`
	UID     string `json:"uid,omitempty"`
	// CorpID is the organization the exchange reported for the token.
	CorpID string `json:"corpId,omitempty"`
}

// sdkClients maps an exchanged directory to its Client. An entry lives as
// long as its directory: callers remove directories, and every Exchange
// drops the entries whose directory is gone.
var sdkClients sync.Map

func writeSDKSession(dir string, session sdkSession) error {
	raw, err := json.Marshal(session)
	if err != nil {
		return errors.New("encode DWS SDK session")
	}
	if err := os.WriteFile(filepath.Join(dir, sdkSessionFile), raw, 0o600); err != nil {
		return errors.New("store DWS SDK session")
	}
	return nil
}

// readSDKSession reports whether dir was exchanged through the SDK.
func readSDKSession(dir string) (sdkSession, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, sdkSessionFile))
	if errors.Is(err, os.ErrNotExist) {
		return sdkSession{}, false, nil
	}
	if err != nil {
		return sdkSession{}, true, errors.New("read DWS SDK session")
	}
	var session sdkSession
	if json.Unmarshal(raw, &session) != nil || session.Gateway == "" || session.MCP == "" {
		return sdkSession{}, true, errors.New("DWS SDK session is unreadable")
	}
	return session, true, nil
}

// sdkEndpoints is the DWS host dws would use and its tool gateway. The
// delivery environment wins over the explicit MCP base URL, as it does for
// the CLI (prepareEnvironment rewrites mcp_url, commandEnv sets
// DWS_MCP_URL), then production. dws maps mcp.* to mcp-gw.* and pre-mcp.* to
// pre-mcp-gw.* the same way.
func (c CLI) sdkEndpoints() (string, string, error) {
	base := strings.TrimSpace(c.MCPBaseURL)
	if c.Environment != "" {
		mcp, _, err := sendEnvironment(c.Environment)
		if err != nil {
			return "", "", err
		}
		base = mcp
	}
	if base == "" {
		base = "https://mcp.dingtalk.com"
	}
	endpoint, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" {
		return "", "", errors.New("invalid DWS MCP base URL")
	}
	host := endpoint.Host
	switch {
	case strings.HasPrefix(host, "pre-mcp."):
		host = "pre-mcp-gw." + strings.TrimPrefix(host, "pre-mcp.")
	case strings.HasPrefix(host, "mcp."):
		host = "mcp-gw." + strings.TrimPrefix(host, "mcp.")
	default:
		return "", "", errors.New("unrecognised DWS MCP host")
	}
	return "https://" + endpoint.Host, "https://" + host, nil
}

// sdkTestConfig lets tests point the SDK at fakes; nil in production.
var sdkTestConfig func(*dws.Config)

func (c CLI) sdkConfig(mcp, gateway string) dws.Config {
	cfg := dws.Config{ClientSecret: strings.TrimSpace(c.ClientSecret), SkipVerify: true, AuthURL: mcp, GatewayURL: gateway}
	if sdkTestConfig != nil {
		sdkTestConfig(&cfg)
	}
	return cfg
}

// exchangeSDK is Exchange through the SDK: the AuthCode becomes a user token
// held by dir's Client.
func (c CLI) exchangeSDK(ctx context.Context, dir string, credential Credential) error {
	mcp, gateway, err := c.sdkEndpoints()
	if err != nil {
		return err
	}
	client, err := dws.New(ctx, c.sdkConfig(mcp, gateway), dws.AuthCode{Code: credential.AuthCode, ClientID: credential.ClientID})
	if err != nil {
		// InitError never carries the code, token or secret; keep the CLI's
		// bounded failure all the same.
		return commandFailed(ctx, "DWS AuthCode exchange failed", err)
	}
	if err := writeSDKSession(dir, sdkSession{MCP: mcp, Gateway: gateway, UID: credential.UID, CorpID: client.Token().CorpID}); err != nil {
		return err
	}
	sdkClients.Store(filepath.Clean(dir), client)
	return nil
}

// sdkClient returns dir's Client. ok is false when dir was exchanged by the
// CLI, which then serves the call.
func sdkClient(dir string) (*dws.Client, sdkSession, bool, error) {
	session, ok, err := readSDKSession(dir)
	if !ok || err != nil {
		return nil, session, ok, err
	}
	client, found := sdkClients.Load(filepath.Clean(dir))
	if !found {
		return nil, session, true, errors.New("DWS SDK session is gone")
	}
	return client.(*dws.Client), session, true, nil
}

func pruneSDKClients() {
	sdkClients.Range(func(key, _ any) bool {
		if _, err := os.Stat(filepath.Join(key.(string), sdkSessionFile)); errors.Is(err, os.ErrNotExist) {
			sdkClients.Delete(key)
		}
		return true
	})
}

// SDKSession reports whether dir was opened on an SDK client.
func SDKSession(dir string) bool {
	_, ok, _ := readSDKSession(dir)
	return ok
}
