package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDWSMessagePolicyClaimAndCustomEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PATH shim")
	}
	t.Parallel()
	var task Task
	if err := json.Unmarshal([]byte(`{"id":"fixture-task","dingtalk_message_policy":{"show_ai_tag":true,"platform_managed_lifecycle":true},"agent":{"custom_env":{"PATH":"/test/custom/bin","MULTICA_DINGTALK_MESSAGE_POLICY":"bad"}}}`), &task); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"PATH": "/test/trusted/bin"}
	layerCustomEnvAndHermesHome(env, task.Agent.CustomEnv, "", nil)
	execenv.ApplyDWSMessagePolicyEnv(env, task.DingTalkMessagePolicy)
	root := t.TempDir()
	if err := installTaskDWSShim(env, root, true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(env["PATH"], filepath.Join(root, "dws-shim")+string(os.PathListSeparator)) || !strings.HasSuffix(env["PATH"], "/test/trusted/bin") {
		t.Fatalf("trusted PATH lost or custom PATH bypassed shim: %q", env["PATH"])
	}
	var policy protocol.DingTalkMessagePolicy
	if err := json.Unmarshal([]byte(env[execenv.DWSMessagePolicyEnv]), &policy); err != nil || !policy.ShowAITag || !policy.PlatformManagedLifecycle {
		t.Fatalf("trusted policy overwritten: %#v, %v", policy, err)
	}
	var legacy Task
	if err := json.Unmarshal([]byte(`{"id":"old-server"}`), &legacy); err != nil || legacy.DingTalkMessagePolicy != nil {
		t.Fatalf("old server claim incompatible: %#v, %v", legacy, err)
	}
	execenv.ApplyDWSMessagePolicyEnv(env, legacy.DingTalkMessagePolicy)
	if env[execenv.DWSMessagePolicyEnv] != "" {
		t.Fatal("legacy task retained prior policy")
	}
}

func TestDWSMessagePolicyRequiresWorkingShim(t *testing.T) {
	t.Parallel()
	if err := installTaskDWSShim(map[string]string{}, "", true); err == nil {
		t.Fatal("managed task must not silently skip policy enforcement")
	}
	if err := installTaskDWSShim(map[string]string{}, "", false); err != nil {
		t.Fatalf("legacy task should keep existing behavior: %v", err)
	}
}
