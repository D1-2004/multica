package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

const dshAuthorityDomain = "multica-dsh-native-authority-v1\n"

var dshAuthorityRequestID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type dshNativeAuthorityBridge struct {
	key     ed25519.PrivateKey
	mu      sync.Mutex
	workers map[string]*dshAuthorityWorker
}
type dshAuthorityWorker struct {
	until  time.Time
	ready  chan struct{}
	failed bool
}
type dshAuthorityRequest struct {
	ID       string          `json:"id"`
	Token    string          `json:"token"`
	Exchange bool            `json:"exchange"`
	Prompt   json.RawMessage `json:"prompt,omitempty"`
	Workdir  string          `json:"workdir,omitempty"`
	Source   string          `json:"source,omitempty"`
}
type dshAuthorityPacket struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// A domain-separated signing key is stable across replicas and restarts. Only
// the public key enters the sandbox; no JWT secret or signing seed is exported.
func newDSHNativeAuthorityBridge(secret string) *dshNativeAuthorityBridge {
	if secret == "" {
		return nil
	}
	seed, err := hkdf.Key(sha256.New, []byte(secret), nil, "multica/dsh/native/authority/v1", ed25519.SeedSize)
	if err != nil {
		return nil
	}
	return &dshNativeAuthorityBridge{key: ed25519.NewKeyFromSeed(seed), workers: make(map[string]*dshAuthorityWorker)}
}
func (b *dshNativeAuthorityBridge) publicKey() string {
	if b == nil {
		return ""
	}
	return hex.EncodeToString(b.key.Public().(ed25519.PublicKey))
}
func (b *dshNativeAuthorityBridge) ensure(ctx context.Context, origin, authority, token string, host dshhost.Host, manager dshhost.NativeAccessManager) error {
	return b.ensureTransport(ctx, origin, authority, token, host, manager, nil, false)
}
func (b *dshNativeAuthorityBridge) ensureTransport(ctx context.Context, origin, authority, token string, host dshhost.Host, manager dshhost.NativeAccessManager, submit DSHNativePromptSubmit, inputLane bool) error {
	if b == nil || manager.Store == nil || manager.CheckManage == nil || (inputLane && submit == nil) {
		return errors.New("DSH authority is unavailable")
	}
	key := strconv.FormatBool(inputLane) + "/" + host.WorkspaceID.String() + "/" + host.AgentID.String() + "/" + strconv.FormatInt(host.Generation, 10) + "/" + authority + "/" + host.SandboxID + "/" + origin + "/" + token
	b.mu.Lock()
	worker := b.workers[key]
	if worker == nil {
		worker = &dshAuthorityWorker{ready: make(chan struct{})}
		b.workers[key] = worker
		go b.serve(key, worker, origin, authority, token, host, manager, submit, inputLane)
	}
	worker.until = time.Now().Add(dshhost.NativeSessionLifetime + dshhost.NativeEntryLifetime + time.Minute)
	b.mu.Unlock()
	select {
	case <-ctx.Done():
		return errors.New("DSH authority connection was not confirmed")
	case <-worker.ready:
		b.mu.Lock()
		failed := worker.failed
		b.mu.Unlock()
		if failed {
			return errors.New("DSH authority connection was rejected")
		}
		return nil
	}
}
func (b *dshNativeAuthorityBridge) sign(request dshAuthorityRequest, result map[string]any, authority string) dshAuthorityPacket {
	raw, _ := json.Marshal(map[string]any{"authority": authority, "id": request.ID, "result": result})
	return dshAuthorityPacket{base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(ed25519.Sign(b.key, append([]byte(dshAuthorityDomain), raw...)))}
}
func (b *dshNativeAuthorityBridge) answer(ctx context.Context, request dshAuthorityRequest, host dshhost.Host, manager dshhost.NativeAccessManager, authority string) dshAuthorityPacket {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var access dshhost.NativeAccess
	var session string
	var err error
	if request.Exchange {
		access, session, err = manager.Exchange(ctx, request.Token, host)
	} else {
		access, err = manager.Authorize(ctx, request.Token, host)
	}
	result := map[string]any{"denied": true}
	if err == nil {
		result = map[string]any{"access_id": access.ID, "user_id": access.UserID, "workspace_id": access.WorkspaceID, "agent_id": access.AgentID, "generation": access.Generation, "sandbox_id": access.SandboxID, "expires_at": access.ExpiresAt}
		if request.Exchange {
			result["session_token"] = session
		}
	}
	return b.sign(request, result, authority)
}
func dshAuthorityPoll(ctx context.Context, client *http.Client, origin, token, controller string, responses []dshAuthorityPacket, wait bool) ([]dshAuthorityRequest, error) {
	return dshNativePoll(ctx, client, origin, token, controller, responses, wait, false)
}
func dshNativePoll(ctx context.Context, client *http.Client, origin, token, controller string, responses []dshAuthorityPacket, wait, inputLane bool) ([]dshAuthorityRequest, error) {
	path, limit, count := "/_multica/authority", int64(65536), 32
	if inputLane {
		path, limit, count = "/_multica/inputs", 4*1024*1024, 1
	}
	body, err := json.Marshal(map[string]any{"controller_id": controller, "responses": responses, "wait": wait})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Multica-Authority-Transport", token)
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("DSH authority transport failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || response.StatusCode != http.StatusOK || int64(len(raw)) > limit {
		return nil, errors.New("DSH authority poll rejected")
	}
	var envelope struct {
		Version  int                   `json:"version"`
		Requests []dshAuthorityRequest `json:"requests"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.Version != 1 || len(envelope.Requests) > count {
		return nil, errors.New("invalid DSH authority poll")
	}
	seen := map[string]bool{}
	for _, r := range envelope.Requests {
		hostInput := inputLane && r.Source == "host" && r.Token == "" && !r.Exchange && len(r.Prompt) > 0
		browserInput := r.Source == "" && len(r.Token) == 48 && (!inputLane || (!r.Exchange && len(r.Prompt) > 0 && strings.HasPrefix(r.Token, "dngs_"))) && (inputLane || len(r.Prompt) == 0)
		if !dshAuthorityRequestID.MatchString(r.ID) || seen[r.ID] || (!hostInput && !browserInput) {
			return nil, errors.New("invalid DSH authority request")
		}
		seen[r.ID] = true
	}
	return envelope.Requests, nil
}
func (b *dshNativeAuthorityBridge) serve(key string, worker *dshAuthorityWorker, origin, authority, token string, host dshhost.Host, manager dshhost.NativeAccessManager, submit DSHNativePromptSubmit, inputLane bool) {
	established := false
	defer func() {
		b.mu.Lock()
		worker.failed = true
		delete(b.workers, key)
		if !established {
			close(worker.ready)
		}
		b.mu.Unlock()
	}()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer transport.CloseIdleConnections()
	controller := uuid.NewString()
	controller = strings.ReplaceAll(controller, "-", "")
	responses := []dshAuthorityPacket{}
	for {
		b.mu.Lock()
		until := worker.until
		b.mu.Unlock()
		if time.Now().After(until) {
			return
		}
		requests, err := dshNativePoll(context.Background(), client, origin, token, controller, responses, established, inputLane)
		if err != nil {
			return
		} // No retry of an uncertain exchange or response delivery.
		if !established {
			b.mu.Lock()
			established = true
			close(worker.ready)
			b.mu.Unlock()
		}
		responses = make([]dshAuthorityPacket, len(requests))
		var group sync.WaitGroup
		for index, request := range requests {
			group.Add(1)
			go func() {
				defer group.Done()
				if inputLane {
					responses[index] = b.answerInput(context.Background(), request, host, manager, authority, submit)
				} else {
					responses[index] = b.answer(context.Background(), request, host, manager, authority)
				}
			}()
		}
		group.Wait()
	}
}

// EnsureDSHNativeAuthority starts independent authorization and input transports. It never
// creates, renews, destroys, leases or replaces an employee writer.
func (l *FCE2BLauncher) EnsureDSHNativeAuthority(ctx context.Context, host dshhost.Host, manager dshhost.NativeAccessManager, submit DSHNativePromptSubmit) (string, error) {
	return l.ensureDSHNativeAuthority(ctx, host, manager, submit, false)
}

// Refresh only the new standard service. Existing images retain their browser lifecycle.
func (l *FCE2BLauncher) EnsureDSHSessionInputs(ctx context.Context, host dshhost.Host, manager dshhost.NativeAccessManager, submit DSHNativePromptSubmit) error {
	if l != nil && l.nativeAuthority != nil {
		l.nativeAuthority.mu.Lock()
		active := false
		for key, worker := range l.nativeAuthority.workers {
			if strings.HasPrefix(key, "true/"+host.WorkspaceID.String()+"/"+host.AgentID.String()+"/"+strconv.FormatInt(host.Generation, 10)+"/") && strings.Contains(key, "/"+host.SandboxID+"/") && !worker.failed {
				worker.until = time.Now().Add(dshhost.NativeSessionLifetime)
				active = true
			}
		}
		l.nativeAuthority.mu.Unlock()
		if active {
			return nil
		}
	}
	_, err := l.ensureDSHNativeAuthority(ctx, host, manager, submit, true)
	return err
}

func (l *FCE2BLauncher) ensureDSHNativeAuthority(ctx context.Context, host dshhost.Host, manager dshhost.NativeAccessManager, submit DSHNativePromptSubmit, managedOnly bool) (string, error) {
	l = l.withCurrentConfig()
	if l == nil || l.nativeAuthority == nil {
		return "", errors.New("DSH authority is unavailable")
	}
	origin, authority, err := dshNativeGatewayAddress(l.Config, host)
	if err != nil {
		return "", err
	}
	out, err := l.dshGatewayControl(ctx, host, "--gateway-authority")
	if err != nil || validateDSHNativeGatewayReceipt(out, host, origin, authority, l.nativeAuthority.publicKey()) != nil {
		return "", errors.New("DSH authority readiness is unconfirmed")
	}
	var receipt struct {
		TransportToken  string `json:"transport_token"`
		InputToken      string `json:"input_transport_token"`
		ManagedSessions int    `json:"managed_sessions"`
	}
	if json.Unmarshal([]byte(out), &receipt) != nil || (!regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(receipt.TransportToken) || !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(receipt.InputToken)) {
		return "", errors.New("DSH authority transport is unavailable")
	}
	if managedOnly && receipt.ManagedSessions != 1 {
		return origin, nil
	}
	if err := l.nativeAuthority.ensure(ctx, origin, authority, receipt.TransportToken, host, manager); err != nil {
		return "", err
	}
	if err := l.nativeAuthority.ensureTransport(ctx, origin, authority, receipt.InputToken, host, manager, submit, true); err != nil {
		return "", err
	}
	return origin, nil
}
