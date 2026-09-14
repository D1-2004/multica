package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

type nativeBridgeStore struct{ access dshhost.NativeAccess }

func (s nativeBridgeStore) InsertNativeAccess(context.Context, dshhost.NativeAccess, string) (dshhost.NativeAccess, error) {
	return s.access, nil
}
func (s nativeBridgeStore) GetNativeAccess(context.Context, string, string) (dshhost.NativeAccess, error) {
	return s.access, nil
}
func (s nativeBridgeStore) ExchangeNativeAccess(context.Context, dshhost.NativeAccess, string, string) (dshhost.NativeAccess, error) {
	return s.access, nil
}
func (s nativeBridgeStore) RevokeNativeAccess(context.Context, dshhost.Key, uuid.UUID) error {
	return nil
}
func TestDSHNativeAuthoritySignsOnlyCurrentHostDecisions(t *testing.T) {
	b := newDSHNativeAuthorityBridge("test-secret")
	if b.publicKey() != newDSHNativeAuthorityBridge("test-secret").publicKey() || b.publicKey() == newDSHNativeAuthorityBridge("other-secret").publicKey() {
		t.Fatal("deployment key derivation is unstable")
	}
	access := dshhost.NativeAccess{ID: uuid.New(), Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, UserID: uuid.New(), Generation: 1, SandboxID: "sbx-test", Kind: "session", ExpiresAt: time.Now().Add(time.Minute)}
	host := dshhost.Host{Key: access.Key, Generation: 1, SandboxID: access.SandboxID, State: "running"}
	allowed := true
	manager := dshhost.NativeAccessManager{Store: nativeBridgeStore{access}, CheckManage: func(context.Context, dshhost.Key, uuid.UUID) error {
		if !allowed {
			return errors.New("revoked")
		}
		return nil
	}}
	request := dshAuthorityRequest{ID: strings.Repeat("a", 32), Token: "dngs_" + strings.Repeat("A", 43)}
	decode := func(packet dshAuthorityPacket) map[string]any {
		t.Helper()
		raw, err := base64.StdEncoding.DecodeString(packet.Payload)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := base64.StdEncoding.DecodeString(packet.Signature)
		if err != nil {
			t.Fatal(err)
		}
		if !ed25519.Verify(b.key.Public().(ed25519.PublicKey), append([]byte(dshAuthorityDomain), raw...), sig) {
			t.Fatal("invalid authority signature")
		}
		if ed25519.Verify(b.key.Public().(ed25519.PublicKey), raw, sig) {
			t.Fatal("signature lost domain separation")
		}
		var envelope struct {
			ID     string         `json:"id"`
			Result map[string]any `json:"result"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.ID != request.ID {
			t.Fatal("wrong request binding")
		}
		return envelope.Result
	}
	result := decode(b.answer(context.Background(), request, host, manager, "https://pre.multica.test"))
	if result["user_id"] != access.UserID.String() || result["session_token"] != nil {
		t.Fatal("invalid check receipt")
	}
	allowed = false
	if decode(b.answer(context.Background(), request, host, manager, "https://pre.multica.test"))["denied"] != true {
		t.Fatal("revoked manager accepted")
	}
	allowed = true
	host.Generation++
	if decode(b.answer(context.Background(), request, host, manager, "https://pre.multica.test"))["denied"] != true {
		t.Fatal("wrong Host accepted")
	}
}
func TestDSHNativeAuthorityPollRejectsRedirectsAndAmbiguousRequests(t *testing.T) {
	body := `{"version":1,"requests":[{"id":"` + strings.Repeat("a", 32) + `","token":"dngs_` + strings.Repeat("A", 43) + `","exchange":false}]}`
	redirected := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_multica/authority" {
			redirected++
			return
		}
		if r.Header.Get("X-Multica-Authority-Transport") != "transport" || r.Header.Get("Authorization") != "" {
			t.Error("wrong authority credentials")
		}
		if body == "redirect" {
			http.Redirect(w, r, "/other", 302)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	result, err := dshAuthorityPoll(context.Background(), client, server.URL, "transport", strings.Repeat("b", 32), []dshAuthorityPacket{}, false)
	if err != nil || len(result) != 1 {
		t.Fatal("valid poll failed", err)
	}
	for _, invalid := range []string{"redirect", `{"version":2,"requests":[]}`, `{"version":1,"requests":[{"id":"bad","token":"x"}]}`, `{"version":1,"requests":[],"extra":1}`, strings.Repeat("x", 65537)} {
		body = invalid
		if _, err := dshAuthorityPoll(context.Background(), client, server.URL, "transport", strings.Repeat("b", 32), []dshAuthorityPacket{}, false); err == nil {
			t.Fatal("invalid poll accepted")
		}
	}
	if redirected != 0 {
		t.Fatal("authority redirect followed")
	}
}

func TestDSHNativeAuthorityWorkerSharesConnectionAndStopsOnFailure(t *testing.T) {
	b := newDSHNativeAuthorityBridge("test-secret")
	host := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Generation: 1, SandboxID: "sbx-worker", State: "running"}
	manager := dshhost.NativeAccessManager{Store: nativeBridgeStore{}, CheckManage: func(context.Context, dshhost.Key, uuid.UUID) error { return errors.New("denied") }}
	first := make(chan struct{})
	stop := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			close(first)
			_, _ = w.Write([]byte(`{"version":1,"requests":[]}`))
			return
		}
		<-stop
		w.WriteHeader(503)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := b.ensure(ctx, server.URL, "https://pre.multica.test", "transport", host, manager); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	<-first
	b.mu.Lock()
	count := len(b.workers)
	b.mu.Unlock()
	if count != 1 {
		t.Fatal("concurrent entry created duplicate worker")
	}
	close(stop)
	deadline := time.Now().Add(time.Second)
	for {
		b.mu.Lock()
		count = len(b.workers)
		b.mu.Unlock()
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed worker not removed")
		}
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 2 {
		t.Fatal("uncertain poll was retried")
	}
}
