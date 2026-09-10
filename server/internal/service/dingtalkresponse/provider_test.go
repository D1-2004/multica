package dingtalkresponse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
)

type fakeIssuer struct {
	requests []agentidentityhsf.CreateContextRequest
}

func (i *fakeIssuer) CreateContext(_ context.Context, in agentidentityhsf.CreateContextRequest) (agentidentityhsf.CreateContextResult, error) {
	i.requests = append(i.requests, in)
	return agentidentityhsf.CreateContextResult{ContextToken: "isolated-context-token"}, nil
}

func TestDWSProviderMintsAndRemovesCredentialsForEveryOperation(t *testing.T) {
	issuer := &fakeIssuer{}
	redeem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer isolated-context-token" {
			t.Error("wrong redemption token")
		}
		_, _ = w.Write([]byte(`{"ok":true,"identity":{"key":"dws","type":"DWS_UID","uid":"123","clientId":"client"},"credential":{"type":"DWS_AUTH_CODE","authCode":"ephemeral-auth-code"}}`))
	}))
	defer redeem.Close()
	dir := t.TempDir()
	capture := filepath.Join(dir, "capture")
	captureJSON, _ := json.Marshal(capture)
	exe := filepath.Join(dir, "fake-dws")
	script := `#!/usr/bin/env python3
import json,os,sys
config=os.environ['DWS_CONFIG_DIR']
assert os.stat(config).st_mode & 0o777 == 0o700
assert 'AGENT_IDENTITY_CONTEXT_TOKEN' not in os.environ
with open(` + string(captureJSON) + `, 'a') as f: f.write(config+'\n')
if sys.argv[1:3] == ['auth','exchange']:
    assert os.environ['DWS_CLIENT_SECRET']=='server-client-secret'
    assert sys.argv[sys.argv.index('--code')+1]=='ephemeral-auth-code'
    with open(os.path.join(config,'token'),'w') as f: f.write('short-lived-token')
elif sys.argv[1:3] == ['chat','+messages-reply']:
    assert os.path.exists(os.path.join(config,'token'))
    assert sys.argv[sys.argv.index('--message-id')+1]=='msg-origin'
    assert sys.argv[sys.argv.index('--group')+1]=='cid'
    print(json.dumps({'success':True,'openTaskId':'quoted-task'}))
elif sys.argv[1:4] == ['chat','message','send']:
    assert os.path.exists(os.path.join(config,'token'))
    assert '--ai-tag=false' in sys.argv
    assert sys.argv[sys.argv.index('--content')+1]=='<@sender> hello'
    print(json.dumps({'success':True,'openTaskId':'sent-task'}))
elif sys.argv[1:4] == ['chat','message','query-send-status']:
    assert os.path.exists(os.path.join(config,'token'))
    print(json.dumps({'success':True,'sendStatus':'SUCCESS','openConversationId':'cid','openMessageId':'mid'}))
else: sys.exit(9)
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_IDENTITY_CONTEXT_TOKEN", "ambient-token")
	p := NewDWSProvider(DWSConfig{AgentIdentity: issuer, BaseURL: redeem.URL, ClientSecret: "server-client-secret", CLIPath: exe, HTTPClient: redeem.Client()})
	in := inputFixture()
	result, err := p.Send(context.Background(), in, "stable-key")
	if err != nil || result.OpenTaskID != "sent-task" {
		t.Fatalf("send=%+v err=%v", result, err)
	}
	status, err := p.Query(context.Background(), in, result.OpenTaskID)
	if err != nil || status.State != "delivered" {
		t.Fatalf("query=%+v err=%v", status, err)
	}
	if len(issuer.requests) != 2 || issuer.requests[0].RequestID == issuer.requests[1].RequestID {
		t.Fatalf("contexts=%+v", issuer.requests)
	}
	for _, req := range issuer.requests {
		if req.UID != in.DWSUID || req.OrgID != in.DWSOrgID || req.AgentID != in.AgentID || req.RuntimeType != "SERVER" {
			t.Fatalf("wrong identity: %+v", req)
		}
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	paths := strings.Fields(string(raw))
	if len(paths) != 4 || paths[0] != paths[1] || paths[2] != paths[3] || paths[0] == paths[2] {
		t.Fatalf("config scopes=%v", paths)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("credential directory retained: %s", path)
		}
	}
}

func TestDWSProviderQuoteReplyUsesOriginMessage(t *testing.T) {
	issuer := &fakeIssuer{}
	redeem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"identity":{"key":"dws","type":"DWS_UID","uid":"123","clientId":"client"},"credential":{"type":"DWS_AUTH_CODE","authCode":"ephemeral-auth-code"}}`))
	}))
	defer redeem.Close()
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-dws")
	script := `#!/usr/bin/env python3
import json,os,sys
if sys.argv[1:3] == ['auth','exchange']:
    raise SystemExit(0)
if sys.argv[1:3] != ['chat','+messages-reply']:
    raise SystemExit(9)
assert sys.argv[sys.argv.index('--message-id')+1]=='msg-origin'
assert sys.argv[sys.argv.index('--group')+1]=='cid'
print(json.dumps({'success':True,'openTaskId':'quoted-task'}))
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := NewDWSProvider(DWSConfig{AgentIdentity: issuer, BaseURL: redeem.URL, ClientSecret: "server-client-secret", CLIPath: exe, HTTPClient: redeem.Client()})
	in := inputFixture()
	in.ReplyToOpenMsgID = "msg-origin"
	result, err := p.Send(context.Background(), in, "stable-key")
	if err != nil || result.OpenTaskID != "quoted-task" {
		t.Fatalf("send=%+v err=%v", result, err)
	}
}

func TestDWSProviderRejectsRedeemedIdentityMismatchBeforeSend(t *testing.T) {
	redeem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"identity":{"key":"dws","type":"DWS_UID","uid":"456","clientId":"client"},"credential":{"type":"DWS_AUTH_CODE","authCode":"auth"}}`))
	}))
	defer redeem.Close()
	p := NewDWSProvider(DWSConfig{AgentIdentity: &fakeIssuer{}, BaseURL: redeem.URL, CLIPath: filepath.Join(t.TempDir(), "must-not-exist"), HTTPClient: redeem.Client()})
	_, err := p.Send(context.Background(), inputFixture(), "stable-key")
	var notSubmitted *NotSubmittedError
	if !errors.As(err, &notSubmitted) || !strings.Contains(notSubmitted.Err.Error(), "identity changed") {
		t.Fatalf("error=%v", err)
	}
}
