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
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func inputBridgeFixture() (*dshNativeAuthorityBridge, dshhost.Host, dshhost.NativeAccessManager, dshAuthorityRequest) {
	b := newDSHNativeAuthorityBridge("fixture")
	access := dshhost.NativeAccess{ID: uuid.New(), Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, UserID: uuid.New(), Generation: 1, SandboxID: "sbx-input", Kind: "session", ExpiresAt: time.Now().Add(time.Minute)}
	host := dshhost.Host{Key: access.Key, Generation: 1, SandboxID: access.SandboxID, State: "running"}
	manager := dshhost.NativeAccessManager{Store: nativeBridgeStore{access}, CheckManage: func(context.Context, dshhost.Key, uuid.UUID) error { return nil }}
	text := "original"
	p := protocol.DSHNativePrompt{SessionID: "session-" + uuid.NewString(), RequestID: uuid.NewString(), Mode: "queue", Content: []protocol.DSHNativePromptPart{{Type: "text", Text: &text}}}
	raw, _ := json.Marshal(p)
	return b, host, manager, dshAuthorityRequest{ID: strings.Repeat("a", 32), Token: "dngs_" + strings.Repeat("A", 43), Prompt: raw}
}
func inputReceipt(input DSHNativeChatInput) DSHNativePromptReceipt {
	id := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
	return DSHNativePromptReceipt{SessionID: input.SessionID, RequestID: input.RequestID, ChatSessionID: id(), TaskID: id(), MessageID: id()}
}
func TestDSHNativeInputRequiresAuthorizationAndCorrelatedDurableReceipt(t *testing.T) {
	for _, mode := range []string{"new", "replay", "denied", "invalid-input", "invalid-receipt", "busy-steer", "conflict", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			b, host, manager, request := inputBridgeFixture()
			called := false
			if mode == "denied" {
				manager.CheckManage = func(context.Context, dshhost.Key, uuid.UUID) error { return errors.New("private denial") }
			}
			if mode == "invalid-input" {
				request.Prompt = []byte(`{"model":"foreign"}`)
			}
			submit := func(_ context.Context, access dshhost.NativeAccess, input DSHNativeChatInput, content string) (DSHNativePromptReceipt, error) {
				called = true
				if access.Key != host.Key || input.Prompt == nil || content != "original" || input.Workdir != "/mnt/multica/workspaces/"+input.SessionID {
					t.Fatal("identity or native input changed")
				}
				receipt := inputReceipt(input)
				switch mode {
				case "replay":
					receipt.Replayed = true
				case "invalid-receipt":
					receipt.RequestID = uuid.New()
				case "busy-steer":
					return receipt, ErrDSHNativeSteerBusy
				case "conflict":
					return receipt, dshhost.ErrChanged
				case "unknown":
					return receipt, errors.New("private database details")
				}
				return receipt, nil
			}
			packet := b.answerInput(context.Background(), request, host, manager, "https://pre.multica.test", submit)
			raw, err := base64.StdEncoding.DecodeString(packet.Payload)
			if err != nil {
				t.Fatal(err)
			}
			signature, err := base64.StdEncoding.DecodeString(packet.Signature)
			if err != nil {
				t.Fatal(err)
			}
			key := b.key.Public().(ed25519.PublicKey)
			if !ed25519.Verify(key, append([]byte(dshInputDomain), raw...), signature) || ed25519.Verify(key, append([]byte(dshAuthorityDomain), raw...), signature) {
				t.Fatal("input and access signatures are not separated")
			}
			var value struct {
				ID     string `json:"id"`
				Result struct {
					Status  int                     `json:"status"`
					Receipt *DSHNativePromptReceipt `json:"receipt"`
				} `json:"result"`
			}
			if json.Unmarshal(raw, &value) != nil || value.ID != request.ID {
				t.Fatal("transport request correlation lost")
			}
			want := map[string]int{"new": 201, "replay": 200, "denied": 403, "invalid-input": 400, "invalid-receipt": 503, "busy-steer": 409, "conflict": 409, "unknown": 503}[mode]
			if value.Result.Status != want || strings.Contains(string(raw), "private") {
				t.Fatalf("unsafe admission result: status=%d", value.Result.Status)
			}
			if (mode == "denied" || mode == "invalid-input") && called {
				t.Fatal("rejected request reached admission")
			}
			if want >= 400 && value.Result.Receipt != nil {
				t.Fatal("failed input returned an accepted receipt")
			}
		})
	}
}

func TestDSHNativeInputDoesNotDelayAccessChecks(t *testing.T) {
	b, host, manager, request := inputBridgeFixture()
	started, release, accessDone, stop := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var inputCalls, accessCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_multica/inputs" {
			if inputCalls.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "requests": []dshAuthorityRequest{request}})
				return
			}
		} else if r.URL.Path == "/_multica/authority" {
			if accessCalls.Add(1) == 1 {
				authRequest := request
				authRequest.Prompt = nil
				_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "requests": []dshAuthorityRequest{authRequest}})
				return
			}
			var envelope struct {
				Responses []dshAuthorityPacket `json:"responses"`
			}
			_ = json.NewDecoder(r.Body).Decode(&envelope)
			if len(envelope.Responses) == 1 {
				close(accessDone)
			}
		} else {
			t.Error("unexpected transport path")
		}
		<-stop
		w.WriteHeader(503)
	}))
	defer server.Close()
	defer close(stop)
	defer close(release)
	submit := func(_ context.Context, _ dshhost.NativeAccess, input DSHNativeChatInput, _ string) (DSHNativePromptReceipt, error) {
		close(started)
		<-release
		return inputReceipt(input), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := b.ensureTransport(ctx, server.URL, "https://pre.multica.test", "input-token", host, manager, submit, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("input did not start")
	}
	if err := b.ensure(ctx, server.URL, "https://pre.multica.test", "access-token", host, manager); err != nil {
		t.Fatal(err)
	}
	select {
	case <-accessDone:
	case <-ctx.Done():
		t.Fatal("slow admission blocked ongoing authorization")
	}
}

func TestDSHNativeInputPollKeepsItsOwnSizeAndBatchLimits(t *testing.T) {
	_, _, _, request := inputBridgeFixture()
	p, err := protocol.DecodeDSHNativePrompt(request.Prompt)
	if err != nil {
		t.Fatal(err)
	}
	*p.Content[0].Text = strings.Repeat("界", 30000)
	request.Prompt, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	requests := []dshAuthorityRequest{request}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "requests": requests})
	}))
	defer server.Close()
	client := server.Client()
	got, err := dshNativePoll(context.Background(), client, server.URL, "fixture", strings.Repeat("b", 32), nil, false, true)
	if err != nil || len(got) != 1 || string(got[0].Prompt) != string(request.Prompt) {
		t.Fatal("full input poll failed", err)
	}
	if _, err := dshNativePoll(context.Background(), client, server.URL, "fixture", strings.Repeat("b", 32), nil, false, false); err == nil {
		t.Fatal("access lane accepted a large input payload")
	}
	duplicate := request
	duplicate.ID = strings.Repeat("c", 32)
	requests = append(requests, duplicate)
	if _, err := dshNativePoll(context.Background(), client, server.URL, "fixture", strings.Repeat("b", 32), nil, false, true); err == nil {
		t.Fatal("input batch exceeded one request")
	}
	requests = requests[:1]
	requests[0].Prompt = nil
	if _, err := dshNativePoll(context.Background(), client, server.URL, "fixture", strings.Repeat("b", 32), nil, false, true); err == nil {
		t.Fatal("input lane accepted an authorization request")
	}
}

func TestDSHNativeInputSelectedWorkdir(t *testing.T) {
	for _, workdir := range []string{"/mnt/multica/files", "/tmp/outside"} {
		b, host, manager, request := inputBridgeFixture()
		request.Workdir = workdir
		called := false
		b.answerInput(context.Background(), request, host, manager, "https://pre.test", func(_ context.Context, _ dshhost.NativeAccess, input DSHNativeChatInput, _ string) (DSHNativePromptReceipt, error) {
			called = true
			if input.Workdir != workdir {
				t.Fatal("selected native directory was changed")
			}
			return inputReceipt(input), nil
		})
		if called != (workdir == "/mnt/multica/files") {
			t.Fatal("unexpected admission for workspace")
		}
	}
}

func TestDSHNativeHostInputPreservesPluginRequestID(t *testing.T) {
	b, host, manager, request := inputBridgeFixture()
	prompt, err := protocol.DecodeDSHNativePrompt(request.Prompt)
	if err != nil {
		t.Fatal(err)
	}
	prompt.RequestID = "weixin-original-request"
	request.Prompt, _ = json.Marshal(prompt)
	request.Source, request.Token = "host", ""
	manager.CheckManage = func(context.Context, dshhost.Key, uuid.UUID) error {
		t.Fatal("host borrowed browser authorization")
		return nil
	}
	expected, _ := protocol.DSHNativeRequestIdentity(prompt.SessionID, prompt.RequestID)
	called := false
	submit := func(_ context.Context, access dshhost.NativeAccess, input DSHNativeChatInput, _ string) (DSHNativePromptReceipt, error) {
		called = true
		if access.Kind != "host" || access.UserID != uuid.Nil || access.Key != host.Key || access.Generation != host.Generation {
			t.Fatal("host principal changed")
		}
		if input.RequestID != expected || input.Prompt.RequestID != prompt.RequestID {
			t.Fatal("plugin correlation lost")
		}
		return inputReceipt(input), nil
	}
	packet := b.answerInput(context.Background(), request, host, manager, "https://pre.test", submit)
	raw, _ := base64.StdEncoding.DecodeString(packet.Payload)
	if !called || !strings.Contains(string(raw), `"status":201`) {
		t.Fatal("host admission failed")
	}
	called = false
	request.Token = "dngs_" + strings.Repeat("A", 43)
	b.answerInput(context.Background(), request, host, manager, "https://pre.test", submit)
	if called {
		t.Fatal("mixed principal reached admission")
	}
}
