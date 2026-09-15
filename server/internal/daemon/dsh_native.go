package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func managedDSHNativeConfig(launchedBy, provider, executable string, custom bool, task Task, getenv func(string) string) (*agent.DSHNativeHostConfig, error) {
	present := false
	for _, key := range []string{"MULTICA_DSH_WORKSPACE_ID", "MULTICA_DSH_AGENT_ID", "MULTICA_DSH_SESSION_ID", "MULTICA_DSH_REQUEST_ID", "MULTICA_DSH_WORKDIR", "MULTICA_DSH_HOST_GENERATION"} {
		present = present || getenv(key) != ""
	}
	if !present {
		if task.DSHNativePrompt != nil {
			return nil, errors.New("native DSH input requires a managed Host")
		}
		return nil, nil
	}
	if launchedBy != "fc-e2b" || provider != "opencode" || custom || executable != "/usr/local/libexec/multica-dsh" || getenv("MULTICA_RUNNER_PROVIDER") != "dsh" || getenv("MULTICA_CLOUD_SANDBOX_BACKEND") != "aliyun_fc" || getenv("DSH_HOME") != "/mnt/multica/home" {
		return nil, errors.New("invalid native DSH launch boundary")
	}
	value := &agent.DSHNativeHostConfig{WorkspaceID: getenv("MULTICA_DSH_WORKSPACE_ID"), AgentID: getenv("MULTICA_DSH_AGENT_ID"), SessionID: getenv("MULTICA_DSH_SESSION_ID"), RequestID: getenv("MULTICA_DSH_REQUEST_ID"), WorkDir: getenv("MULTICA_DSH_WORKDIR")}
	for _, id := range []string{value.WorkspaceID, value.AgentID, value.RequestID} {
		if parsed, err := uuid.Parse(id); err != nil || parsed == uuid.Nil || parsed.String() != id {
			return nil, errors.New("invalid native DSH launch identity")
		}
	}
	if !protocol.ValidDSHSessionID(value.SessionID) {
		return nil, errors.New("invalid native DSH Session identity")
	}
	if task.WorkspaceID != value.WorkspaceID || task.AgentID != value.AgentID || (getenv("MULTICA_TASK_ID") != "" && getenv("MULTICA_TASK_ID") != task.ID) {
		return nil, errors.New("native DSH launch does not match claimed task")
	}
	generation, err := strconv.ParseInt(getenv("MULTICA_DSH_HOST_GENERATION"), 10, 64)
	if err != nil || generation < 1 {
		return nil, errors.New("invalid native DSH Host generation")
	}
	value.Generation = generation
	if task.DSHNativePrompt != nil {
		copy, err := task.DSHNativePrompt.Clone()
		if err != nil || copy.SessionID != value.SessionID || copy.RequestID != value.RequestID {
			return nil, errors.New("native DSH input does not match claimed binding")
		}
		value.Prompt = copy
	}
	if value.WorkDir != filepath.Join("/mnt/multica/workspaces", value.SessionID) {
		return nil, errors.New("invalid native DSH workspace binding")
	}
	value.ModelBaseURL, value.ModelAPIKey, value.ProviderGeneration = getenv("OPENAI_BASE_URL"), getenv("OPENAI_API_KEY"), getenv("MULTICA_TRACE_ID")
	// Claim credentials expire after 24 hours. Keep the in-memory binding inside
	// that limit; the upstream services remain authoritative for revocation.
	value.ModelID = getenv("OPENAI_MODEL")
	value.ExpiresAt = time.Now().Add(23 * time.Hour)
	return value, nil
}
func nativeDSHToolEnvironment(child map[string]string) map[string]string {
	result := map[string]string{}
	for _, key := range []string{"MULTICA_TOKEN", "MULTICA_TASK_ID", "MULTICA_SERVER_URL", "MULTICA_WORKSPACE_ID", "MULTICA_DEAP_DWS_TOKEN", "MULTICA_A2A_INVOCATION", "DWS_CONFIG_DIR", "GH_CONFIG_DIR"} {
		if value, ok := child[key]; ok {
			result[key] = value
		}
	}
	return result
}
func loadManagedDSHNativeConfig(launchedBy, provider, executable string, custom bool, task Task) (*agent.DSHNativeHostConfig, error) {
	return managedDSHNativeConfig(launchedBy, provider, executable, custom, task, os.Getenv)
}
