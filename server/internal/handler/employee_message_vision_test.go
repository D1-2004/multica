package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeeresource"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

func TestEmployeeVisionConfigDecodeIsStrict(t *testing.T) {
	good := `{"executors":[{"runtime_provider":"pi","model":"bailian/deepseek-v4.1-flash","verified_at":"2026-10-03T19:59:47+08:00","evidence":"agent_task 67d3e5f0 read V5106-5DD7"}]}`
	cfg, err := DecodeEmployeeVision(json.RawMessage(good))
	if err != nil || !cfg.allows("pi", "bailian/deepseek-v4.1-flash") || cfg.allows("pi", "mass/glm-5") || cfg.allows("codex", "bailian/deepseek-v4.1-flash") {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	// Absent keeps the probe-verified code default; an explicit empty list
	// disables vision.
	if cfg, err = DecodeEmployeeVision(nil); err != nil || !cfg.allows("pi", "bailian/deepseek-v4.1-flash") || len(cfg.Executors) != 1 {
		t.Fatal("absent config", cfg, err)
	}
	if cfg, err = DecodeEmployeeVision(json.RawMessage(`{"executors":[]}`)); err != nil || cfg.allows("pi", "bailian/deepseek-v4.1-flash") {
		t.Fatal("empty override", cfg, err)
	}
	for _, e := range DefaultEmployeeVisionConfig().Executors {
		raw, _ := json.Marshal(EmployeeVisionConfig{Executors: []EmployeeVisionExecutor{e}})
		if _, err = DecodeEmployeeVision(raw); err != nil {
			t.Fatal("code default fails its own validation", err)
		}
	}
	for name, raw := range map[string]string{
		"unknown field":   `{"executors":[],"models":["x"]}`,
		"bare model":      `{"executors":[{"runtime_provider":"pi","model":"deepseek","verified_at":"2026-10-03T19:59:47+08:00","evidence":"e"}]}`,
		"no provider":     `{"executors":[{"runtime_provider":"","model":"a/b","verified_at":"2026-10-03T19:59:47+08:00","evidence":"e"}]}`,
		"no verification": `{"executors":[{"runtime_provider":"pi","model":"a/b"}]}`,
		"no evidence":     `{"executors":[{"runtime_provider":"pi","model":"a/b","verified_at":"2026-10-03T19:59:47+08:00","evidence":" "}]}`,
		"duplicate":       `{"executors":[{"runtime_provider":"pi","model":"a/b","verified_at":"2026-10-03T19:59:47+08:00","evidence":"e"},{"runtime_provider":"pi","model":"a/b","verified_at":"2026-10-03T19:59:47+08:00","evidence":"e"}]}`,
		"padded model":    `{"executors":[{"runtime_provider":"pi","model":" a/b","verified_at":"2026-10-03T19:59:47+08:00","evidence":"e"}]}`,
	} {
		if _, err := DecodeEmployeeVision(json.RawMessage(raw)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// Images become Deferred only for a verified executor pair of this agent's
// runtime provider and explicit model; the background task then receives the
// Host-verified references of exactly this source, never a signed URL.
func TestEmployeeVisionDeferredImagesReachTheBackgroundPacket(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01")
	r := newEmployeeResourceTest(t, resourceMessage("msg-img", "[图片消息](mediaId=$TEXT) [文件] shapes.png fileId: F-PNG"))
	ctx := context.Background()
	r.dws.messages["msg-img"] = providerMessage(r.conversation, "msg-img", resourceRequester, "", imageRes("@IMAGE-1"), fileRes("F-PNG"))
	r.dws.files["F-PNG"] = dwsclient.MessageFile{Name: "shapes.png", ContentType: "image/png", Data: png}
	worker := r.f.h.EmployeeSceneWorker
	verified := EmployeeVisionConfig{Executors: []EmployeeVisionExecutor{{RuntimeProvider: "codex", Model: "mass/qwen3.8-max", VerifiedAt: "2026-10-03T20:00:00+08:00", Evidence: "probe"}}}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET model='mass/qwen3.8-max' WHERE id=$1`, r.f.agentID); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]func() EmployeeVisionConfig{
		"code default": nil,
		"disabled":     func() EmployeeVisionConfig { return EmployeeVisionConfig{} },
		"other model": func() EmployeeVisionConfig {
			c := verified
			c.Executors = []EmployeeVisionExecutor{{RuntimeProvider: "codex", Model: "mass/glm-5"}}
			return c
		},
		"other executor": func() EmployeeVisionConfig {
			c := verified
			c.Executors = []EmployeeVisionExecutor{{RuntimeProvider: "pi", Model: "mass/qwen3.8-max"}}
			return c
		},
	} {
		worker.VisionConfig = cfg
		if worker.visionReady(ctx, r.job) {
			t.Fatalf("%s: vision ready", name)
		}
	}
	worker.VisionConfig = func() EmployeeVisionConfig { return verified }
	if !worker.visionReady(ctx, r.job) {
		t.Fatal("verified executor pair not ready")
	}
	r.reader.vision = true
	got := r.read(t)
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v", got.Items)
	}
	image, file := got.Items[0], got.Items[1]
	if image.Kind != "image" || image.State != employeeresource.Deferred || image.Reason != employeeresource.ReasonBackgroundVision || image.Text != "" {
		t.Fatalf("image = %+v", image)
	}
	if file.State != employeeresource.Deferred || file.MediaType != "image/png" || file.SHA256 == "" || file.Text != "" {
		t.Fatalf("image file = %+v", file)
	}
	raw, _ := json.Marshal(got)
	if !employeeResourcesDeferred(string(raw)) {
		t.Fatal("deferred context not recognized")
	}

	// The model dispatches; the Work Packet carries both verified references.
	host := &employeeSceneHost{worker: worker, job: r.job, envelopes: r.envs}
	source := employeeSourceMessages(r.job.Items[0], r.envs[0])[0]
	identity := employeeloop.Identity{WorkspaceID: r.job.Scope.WorkspaceID, AgentID: r.job.Scope.AgentID, TenantOrgID: r.job.Scope.TenantOrgID, ReceiptID: r.job.Items[0].ReceiptID}
	identity.Scene.SceneID = r.job.Scope.SceneID
	result, err := host.Execute(ctx, identity, employeeloop.ToolCall{Name: "dispatch_task", NativeToolCallID: "vision-dispatch", Arguments: map[string]any{"source_ref": source.SourceRef, "goal": "看图回答", "prompt": "Open the attached image and report its text and shapes.", "reply": "我来看一下这张图。"}})
	if err != nil || result.Receipt == "" {
		t.Fatal("dispatch", result, err)
	}
	var packet string
	if err = testPool.QueryRow(ctx, `SELECT row_to_json(q)::text FROM agent_task_queue q JOIN employee_task_run r ON r.queue_task_id=q.id WHERE r.id=$1::uuid`, result.Receipt).Scan(&packet); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FORMAL MATERIAL REFERENCES", "@IMAGE-1", "F-PNG", "+messages-resource-download", "dws drive download --node F-PNG", file.SHA256} {
		if !strings.Contains(packet, want) {
			t.Fatalf("packet lacks %q", want)
		}
	}
	for _, leak := range []string{"RESOURCE-SECRET-SIG", "oss.example", "$TEXT-only"} {
		if strings.Contains(packet, leak) {
			t.Fatalf("packet leaks %q", leak)
		}
	}
}
