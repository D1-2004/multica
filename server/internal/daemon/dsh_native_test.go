package daemon

import (
	"github.com/multica-ai/multica/server/pkg/protocol"
	"testing"
)

func TestDSHNativeLaunchMatchesClaimAndNeverRestoresScrubbedIdentity(t *testing.T) {
	const workspace = "dd996f77-e2fd-4a16-84d9-1032b55a05f1"
	const employee = "8ddc9f0f-f9d7-4554-9c9c-dcd464867e4f"
	const session = "31e58f19-8669-42f3-98f6-01bc71aa8ad0"
	env := map[string]string{"MULTICA_DSH_SESSION_ID": session, "MULTICA_DSH_HOST_GENERATION": "7", "MULTICA_RUNNER_PROVIDER": "dsh", "MULTICA_CLOUD_SANDBOX_BACKEND": "aliyun_fc", "DSH_HOME": "/mnt/multica/home", "MULTICA_DSH_WORKSPACE_ID": workspace, "MULTICA_DSH_AGENT_ID": employee, "MULTICA_DSH_REQUEST_ID": "91675c65-a8e3-4b25-85fc-5e82081af8af", "MULTICA_DSH_WORKDIR": "/mnt/multica/workspaces/" + session}
	task := Task{ID: "task", WorkspaceID: workspace, AgentID: employee}
	get := func(key string) string { return env[key] }
	native, err := managedDSHNativeConfig("fc-e2b", "opencode", "/usr/local/libexec/multica-dsh", false, task, get)
	if err != nil || native == nil || native.Generation != 7 {
		t.Fatal("matching launch was not accepted")
	}
	for _, key := range []string{"MULTICA_DSH_WORKSPACE_ID", "MULTICA_DSH_AGENT_ID", "MULTICA_DSH_SESSION_ID", "MULTICA_DSH_WORKDIR", "DSH_HOME", "MULTICA_RUNNER_PROVIDER", "MULTICA_CLOUD_SANDBOX_BACKEND"} {
		original := env[key]
		env[key] = "wrong"
		if _, err := managedDSHNativeConfig("fc-e2b", "opencode", "/usr/local/libexec/multica-dsh", false, task, get); err == nil {
			t.Fatalf("invalid %s accepted", key)
		}
		env[key] = original
	}
	for _, where := range []string{"desktop", ""} {
		if _, err := managedDSHNativeConfig(where, "opencode", "/usr/local/libexec/multica-dsh", false, task, get); err == nil {
			t.Fatal("untrusted launch accepted")
		}
	}
	if _, err := managedDSHNativeConfig("fc-e2b", "opencode", "/usr/local/libexec/multica-dsh", true, task, get); err == nil {
		t.Fatal("custom runtime command acquired native Host")
	}
	child := map[string]string{"MULTICA_TOKEN": "mat_fixture", "MULTICA_WORKSPACE_ID": workspace, "MULTICA_DSH_SESSION_ID": session, "OPENAI_API_KEY": "fixture", "MULTICA_A2A_INVOCATION": "1"}
	isolateA2AChildEnv(child)
	child["MULTICA_A2A_INVOCATION"] = "1"
	child["MULTICA_DEAP_DWS_TOKEN"] = "external-task-fixture"
	tools := nativeDSHToolEnvironment(child)
	if tools["MULTICA_TOKEN"] != "" || tools["MULTICA_WORKSPACE_ID"] != "" || tools["MULTICA_DSH_SESSION_ID"] != "" || tools["OPENAI_API_KEY"] != "" {
		t.Fatal("native bridge restored a scrubbed internal identity")
	}
	if tools["MULTICA_DEAP_DWS_TOKEN"] != "external-task-fixture" {
		t.Fatal("native bridge lost attested task identity")
	}
}

func TestDSHNativeLaunchCarriesTypedInputAndRejectsFallback(t *testing.T) {
	const workspace = "dd996f77-e2fd-4a16-84d9-1032b55a05f1"
	const employee = "8ddc9f0f-f9d7-4554-9c9c-dcd464867e4f"
	const request = "91675c65-a8e3-4b25-85fc-5e82081af8af"
	for _, sid := range []string{"31e58f19-8669-42f3-98f6-01bc71aa8ad0", "session-31e58f19-8669-42f3-98f6-01bc71aa8ad0"} {
		t.Run(sid, func(t *testing.T) {
			receipt := "upload"
			prompt := &protocol.DSHNativePrompt{SessionID: sid, RequestID: request, Mode: "queue", Content: []protocol.DSHNativePromptPart{{Type: "file", ReceiptID: &receipt}}}
			task := Task{ID: "task", WorkspaceID: workspace, AgentID: employee, DSHNativePrompt: prompt}
			env := map[string]string{"MULTICA_DSH_SESSION_ID": sid, "MULTICA_DSH_REQUEST_ID": request, "MULTICA_DSH_WORKSPACE_ID": workspace, "MULTICA_DSH_AGENT_ID": employee, "MULTICA_DSH_HOST_GENERATION": "7", "MULTICA_DSH_WORKDIR": "/mnt/multica/workspaces/" + sid, "DSH_HOME": "/mnt/multica/home", "MULTICA_RUNNER_PROVIDER": "dsh", "MULTICA_CLOUD_SANDBOX_BACKEND": "aliyun_fc"}
			get := func(key string) string { return env[key] }
			config, err := managedDSHNativeConfig("fc-e2b", "opencode", "/usr/local/libexec/multica-dsh", false, task, get)
			if err != nil || config == nil || config.Prompt == nil || config.SessionID != sid {
				t.Fatalf("valid native payload rejected: %v", err)
			}
			receipt = "changed"
			if *config.Prompt.Content[0].ReceiptID != "upload" {
				t.Fatal("caller mutated launch input")
			}
			prompt.RequestID = workspace
			if _, err := managedDSHNativeConfig("fc-e2b", "opencode", "/usr/local/libexec/multica-dsh", false, task, get); err == nil {
				t.Fatal("foreign request accepted")
			}
			if _, err := managedDSHNativeConfig("fc-e2b", "opencode", "/usr/local/libexec/multica-dsh", false, task, func(string) string { return "" }); err == nil {
				t.Fatal("typed input fell back to legacy execution")
			}
		})
	}
}
