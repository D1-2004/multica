package agent

import (
	"context"
	"os"
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
	execute := func(requestID, generation string, timeout time.Duration) (Result, int) {
		value := native
		value.RequestID = requestID
		value.ProviderGeneration = generation
		backend, err := NewDSHNativeHostBackend(Config{}, value)
		if err != nil {
			t.Fatal(err)
		}
		session, err := backend.Execute(context.Background(), "Run the fixture bash check then answer with its marker.", ExecOptions{Cwd: value.WorkDir, Model: "fixture-model", Timeout: timeout})
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
		return result, tools
	}
	first := uuid.NewString()
	for _, item := range []struct{ id, generation string }{{first, "native-go-first"}, {uuid.NewString(), "native-go-second"}} {
		result, tools := execute(item.id, item.generation, 25*time.Second)
		if result.Status != "completed" || result.Output != "NATIVE_GO_COMPLETE" || tools != 1 {
			t.Fatalf("native execute status=%s tools=%d error=%s", result.Status, tools, result.Error)
		}
	}
	// The completed first request is now on an older turn. It must neither
	// submit another prompt nor return the second request's result.
	replay, tools := execute(first, "native-go-replay-must-not-call-model", 25*time.Second)
	if replay.Status != "completed" || replay.Output != "NATIVE_GO_COMPLETE" || tools != 0 {
		t.Fatal("completed request did not replay its terminal result")
	}
	cancelled, _ := execute(uuid.NewString(), "native-go-cancel", 3*time.Second)
	if cancelled.Status != "timeout" {
		t.Fatalf("cancel status=%s error=%s", cancelled.Status, cancelled.Error)
	}
	final, tools := execute(uuid.NewString(), "native-go-after-cancel", 25*time.Second)
	if final.Status != "completed" || tools != 1 {
		t.Fatalf("Session not reusable after confirmed cancellation: %s %s", final.Status, final.Error)
	}
}
