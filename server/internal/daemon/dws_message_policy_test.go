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

func TestDWSFinalTextOwnerClaimSurvivesEnvironmentRoundTrip(t *testing.T) {
	for _, owner := range []string{"host", "", "future-owner"} {
		t.Run(owner, func(t *testing.T) {
			policy := map[string]any{"show_ai_tag": true, "platform_managed_lifecycle": true, "reply_conversation_id": "cid-origin"}
			if owner != "" {
				policy["final_text_owner"] = owner
			}
			raw, err := json.Marshal(map[string]any{"id": "direct-task", "dingtalk_message_policy": policy})
			if err != nil {
				t.Fatal(err)
			}
			var task Task
			if err = json.Unmarshal(raw, &task); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{execenv.DWSMessagePolicyEnv: `{"final_text_owner":"untrusted"}`}
			execenv.ApplyDWSMessagePolicyEnv(env, task.DingTalkMessagePolicy)
			var got map[string]any
			if err = json.Unmarshal([]byte(env[execenv.DWSMessagePolicyEnv]), &got); err != nil {
				t.Fatal(err)
			}
			if owner == "" {
				if _, present := got["final_text_owner"]; present {
					t.Fatal("legacy policy gained a final-text owner")
				}
			} else if got["final_text_owner"] != owner {
				t.Fatal("claim field disappeared before the SDK could read it", got)
			}
			execenv.ApplyDWSMessagePolicyEnv(env, nil)
			if env[execenv.DWSMessagePolicyEnv] != "" {
				t.Fatal("warm task retained prior final ownership")
			}
		})
	}
}
