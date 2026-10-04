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

	"github.com/google/uuid"
)

type cardUpdateProvider struct {
	fakeProvider
	updates int
	err     error
}

func (p *cardUpdateProvider) UpdateQuestionCard(_ context.Context, _ ActionInput, _ string, _ []string) error {
	p.updates++
	return p.err
}

func closedCardFixture(t *testing.T) (ActionInput, []string) {
	t.Helper()
	in := inputFixture()
	in.SceneID = uuid.NewString()
	questionID := uuid.NewString()
	in.A2UICard = &A2UIQuestionCard{QuestionID: questionID, PublicID: "ask:" + questionID}
	raw, err := json.Marshal(map[string]any{"version": "v1.0", "updateComponents": map[string]any{
		"surfaceId": "s-" + questionID,
		"components": []any{
			map[string]any{"id": "root", "component": "Column", "children": []string{"selected"}},
			map[string]any{"id": "selected", "component": "Text", "text": "办公流程 ✓"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return in, []string{string(raw)}
}

func TestQuestionCardUpdateUsesOptionalProviderWithoutSending(t *testing.T) {
	in, messages := closedCardFixture(t)
	p := &cardUpdateProvider{}
	s := NewService(nil, p, nil)
	for range 2 {
		if err := s.UpdateQuestionCard(context.Background(), in, "existing-biz-id", messages); err != nil {
			t.Fatal(err)
		}
	}
	if p.updates != 2 || p.sends.Load() != 0 || p.queries.Load() != 0 {
		t.Fatal("card replacement became a new message", p.updates, p.sends.Load(), p.queries.Load())
	}
	p.err = errors.New("unknown update outcome")
	if err := s.UpdateQuestionCard(context.Background(), in, "existing-biz-id", messages); !errors.Is(err, p.err) {
		t.Fatal("unknown result was acknowledged", err)
	}
	if err := NewService(nil, &fakeProvider{}, nil).UpdateQuestionCard(context.Background(), in, "existing-biz-id", messages); err == nil {
		t.Fatal("missing update capability accepted")
	}
}

func TestQuestionCardUpdateRejectsForeignSurfaceAndInteractiveProjection(t *testing.T) {
	in, messages := closedCardFixture(t)
	for _, test := range []struct {
		name string
		edit func(ActionInput, []string) (ActionInput, string, []string)
	}{
		{"foreign_scope", func(in ActionInput, m []string) (ActionInput, string, []string) {
			in.SceneID = "not-a-scene"
			return in, "biz", m
		}},
		{"no_biz", func(in ActionInput, m []string) (ActionInput, string, []string) { return in, "", m }},
		{"foreign_surface", func(in ActionInput, m []string) (ActionInput, string, []string) {
			return in, "biz", []string{strings.ReplaceAll(m[0], "s-"+in.A2UICard.QuestionID, "s-"+uuid.NewString())}
		}},
		{"new_surface", func(in ActionInput, m []string) (ActionInput, string, []string) {
			return in, "biz", []string{strings.ReplaceAll(m[0], "updateComponents", "createSurface")}
		}},
		{"button", func(in ActionInput, m []string) (ActionInput, string, []string) {
			return in, "biz", []string{strings.ReplaceAll(m[0], `"component":"Text"`, `"component":"Button"`)}
		}},
		{"action_on_text", func(in ActionInput, m []string) (ActionInput, string, []string) {
			return in, "biz", []string{strings.ReplaceAll(m[0], `"component":"Text"`, `"component":"Text","action":{}`)}
		}},
		{"retained_old_button", func(in ActionInput, m []string) (ActionInput, string, []string) {
			return in, "biz", []string{strings.ReplaceAll(m[0], `"children":["selected"]`, `"children":["old-submit"]`)}
		}},
		{"cyclic_tree", func(in ActionInput, m []string) (ActionInput, string, []string) {
			return in, "biz", []string{strings.ReplaceAll(m[0], `"children":["selected"]`, `"children":["root"]`)}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &cardUpdateProvider{}
			got, biz, projection := test.edit(in, append([]string(nil), messages...))
			if err := NewService(nil, p, nil).UpdateQuestionCard(context.Background(), got, biz, projection); err == nil || p.updates != 0 {
				t.Fatal("unsafe card update reached provider", err, p.updates)
			}
		})
	}
}

func TestDWSProviderQuestionCardUpdateAuthenticatesEmployeeAndFinishesOriginal(t *testing.T) {
	issuer := &fakeIssuer{}
	redeem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer isolated-context-token" {
			t.Error("unexpected identity redemption")
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
assert os.environ['DWS_USE_PRE']=='1'
assert os.environ['DWS_MCP_URL']=='https://pre-mcp.dingtalk.com'
with open(` + string(captureJSON) + `,'a') as f: f.write(config+'\n')
if sys.argv[1:3]==['auth','exchange']:
    with open(os.path.join(config,'token'),'w') as f: f.write('ephemeral')
elif sys.argv[1:4]==['chat','message','update-a2ui-card']:
    assert os.path.exists(os.path.join(config,'token'))
    assert sys.argv[sys.argv.index('--biz-id')+1]=='provider-original-biz'
    assert sys.argv[sys.argv.index('--flow-status')+1]=='FINISH'
    assert len(json.loads(sys.argv[sys.argv.index('--content')+1]))==1
    assert json.loads(sys.argv[sys.argv.index('--a2ui-annotations')+1])==[]
    print(json.dumps({'success':True}))
else: raise SystemExit(9)
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := NewDWSProvider(DWSConfig{AgentIdentity: issuer, BaseURL: redeem.URL, ClientSecret: "server-client-secret", CLIPath: exe, HTTPClient: redeem.Client()})
	in, messages := closedCardFixture(t)
	in.DWSEnvironment = "staging"
	if err := NewService(nil, p, nil).UpdateQuestionCard(context.Background(), in, "provider-original-biz", messages); err != nil {
		t.Fatal(err)
	}
	if len(issuer.requests) != 1 || issuer.requests[0].AgentID != in.AgentID || issuer.requests[0].UID != in.DWSUID || issuer.requests[0].OrgID != in.DWSOrgID {
		t.Fatal("update did not authenticate the employee", issuer.requests)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, credentialDir := range strings.Fields(string(raw)) {
		if _, err := os.Stat(credentialDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("update credentials retained", err)
		}
	}
}
