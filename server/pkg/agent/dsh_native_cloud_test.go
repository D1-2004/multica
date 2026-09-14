package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Explicit cloud-only acceptance entry point. Local runs only compile it.
func TestDSHNativeRealHostExecution(t *testing.T) {
	if os.Getenv("DSH_NATIVE_CLOUD_TEST") != "1" {
		t.Skip("requires an actual FC managed Host")
	}
	native := DSHNativeHostConfig{WorkspaceID: os.Getenv("MULTICA_DSH_WORKSPACE_ID"), AgentID: os.Getenv("MULTICA_DSH_AGENT_ID"), Generation: 1, SessionID: os.Getenv("MULTICA_DSH_SESSION_ID"), WorkDir: os.Getenv("MULTICA_DSH_WORKDIR"), ModelBaseURL: "http://127.0.0.1:38127/v1", ModelAPIKey: "cloud-fixture-only", ExpiresAt: time.Now().Add(10 * time.Minute)}
	expectedTools := 1
	skills := os.Getenv("DSH_NATIVE_CLOUD_SKILLS") == "1"
	mcp := os.Getenv("DSH_NATIVE_CLOUD_MCP") == "1"
	if skills {
		expectedTools = 2
	}
	if mcp {
		expectedTools++
	}
	execute := func(value DSHNativeHostConfig, requestID, generation string, timeout time.Duration) (Result, int) {
		value.RequestID = requestID
		value.ProviderGeneration = generation
		var mcpConfig json.RawMessage
		var pidPath string
		if mcp {
			directory := t.TempDir()
			script := filepath.Join(directory, "mcp.py")
			pidPath = filepath.Join(directory, "pid")
			if err := os.WriteFile(script, []byte(nativeCloudMCPFixture), 0600); err != nil {
				t.Fatal(err)
			}
			mcpConfig, _ = json.Marshal(map[string]any{"mcpServers": map[string]any{"native-probe": map[string]any{"command": "/opt/task-python/bin/python3", "args": []string{script}, "env": map[string]string{"NATIVE_MCP_REVISION": generation, "NATIVE_MCP_PID_FILE": pidPath}}}})
		}
		if skills {
			value.ContextText = "NATIVE_CONTEXT_" + generation + " literal {{unregistered_user_template}}"
			value.SkillDirectory = t.TempDir()
			dir := filepath.Join(value.SkillDirectory, "native-go-skill")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			content := "---\nname: native-go-skill\ndescription: Verify current task Skill revision.\n---\nNATIVE_SKILL_" + generation
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
		backend, err := NewDSHNativeHostBackend(Config{}, value)
		if err != nil {
			t.Fatal(err)
		}
		session, err := backend.Execute(context.Background(), "Run the fixture tools then answer with their marker.", ExecOptions{Cwd: value.WorkDir, Model: "fixture-model", Timeout: timeout, McpConfig: mcpConfig})
		if err != nil {
			t.Fatal(err)
		}
		tools := 0
		for message := range session.Messages {
			if message.Type == MessageToolUse {
				tools++
			}
		}
		result := <-session.Result
		if !backend.(interface{ NativeHostTaskQuiescent() bool }).NativeHostTaskQuiescent() {
			t.Fatal("native task cleanup did not confirm quiescence")
		}
		if mcp {
			pid, err := os.ReadFile(pidPath)
			if strings.Contains(generation, "replay") {
				if !os.IsNotExist(err) {
					t.Fatal("completed replay recreated MCP subprocess")
				}
			} else {
				if err != nil || len(pid) == 0 {
					t.Fatal("MCP subprocess did not start")
				}
				if _, err := os.Stat("/proc/" + strings.TrimSpace(string(pid))); !os.IsNotExist(err) {
					t.Fatal("MCP subprocess survived confirmed task cleanup")
				}
			}
		}
		return result, tools
	}
	first := uuid.NewString()
	for _, item := range []struct{ id, generation string }{{first, "native-go-first"}, {uuid.NewString(), "native-go-second"}} {
		result, tools := execute(native, item.id, item.generation, 25*time.Second)
		if result.Status != "completed" || result.Output != "NATIVE_GO_COMPLETE" || tools != expectedTools {
			t.Fatalf("native execute status=%s tools=%d error=%s", result.Status, tools, result.Error)
		}
	}
	// The completed first request is now on an older turn. It must neither
	// submit another prompt nor return the second request's result.
	replay, tools := execute(native, first, "native-go-replay-must-not-call-model", 25*time.Second)
	if replay.Status != "completed" || replay.Output != "NATIVE_GO_COMPLETE" || tools != 0 {
		t.Fatal("completed request did not replay its terminal result")
	}
	cancelled, _ := execute(native, uuid.NewString(), "native-go-cancel", 3*time.Second)
	if cancelled.Status != "timeout" {
		t.Fatalf("cancel status=%s error=%s", cancelled.Status, cancelled.Error)
	}
	final, tools := execute(native, uuid.NewString(), "native-go-after-cancel", 25*time.Second)
	if final.Status != "completed" || tools != expectedTools {
		t.Fatalf("Session not reusable after confirmed cancellation: %s %s", final.Status, final.Error)
	}
	if mcp {
		// Both calls must reach the model's barrier concurrently. Reusing the
		// same MCP server/tool name must preserve each Agent's own revision.
		var wait sync.WaitGroup
		for _, generation := range []string{"native-go-parallel-a", "native-go-parallel-b"} {
			value := native
			value.SessionID = uuid.NewString()
			value.WorkDir = filepath.Join(filepath.Dir(native.WorkDir), value.SessionID)
			if err := os.Mkdir(value.WorkDir, 0700); err != nil {
				t.Fatal(err)
			}
			wait.Add(1)
			go func() {
				defer wait.Done()
				result, tools := execute(value, uuid.NewString(), generation, 25*time.Second)
				if result.Status != "completed" || tools != expectedTools {
					t.Errorf("parallel MCP Session failed: %s tools=%d error=%s", result.Status, tools, result.Error)
				}
			}()
		}
		wait.Wait()
	}
}

// This subprocess is only executed by the opt-in FC test, never locally.
const nativeCloudMCPFixture = `import json,os,sys
from pathlib import Path
Path(os.environ['NATIVE_MCP_PID_FILE']).write_text(str(os.getpid()))
for line in sys.stdin:
    message=json.loads(line)
    if 'id' not in message: continue
    method=message['method']
    if method=='initialize':
        result={'protocolVersion':message['params']['protocolVersion'],'capabilities':{'tools':{}},'serverInfo':{'name':'native-cloud-probe','version':'1'}}
    elif method=='tools/list':
        result={'tools':[{'name':'revision','description':'Read current task fixture revision','inputSchema':{'type':'object','properties':{},'additionalProperties':False}}]}
    elif method=='tools/call':
        result={'content':[{'type':'text','text':'NATIVE_MCP_'+os.environ['NATIVE_MCP_REVISION']}]}
    elif method=='ping': result={}
    else:
        print(json.dumps({'jsonrpc':'2.0','id':message['id'],'error':{'code':-32601,'message':'Method not found'}}),flush=True)
        continue
    print(json.dumps({'jsonrpc':'2.0','id':message['id'],'result':result}),flush=True)
`
