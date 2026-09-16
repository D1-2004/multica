package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// DSHNativeHostConfig comes from the trusted FC launch receipt. It is kept in
// the daemon, outside the environment inherited by an Agent's tools.
type DSHNativeHostConfig struct {
	Prompt                                        *protocol.DSHNativePrompt
	WorkspaceID, AgentID                          string
	Generation                                    int64
	SessionID, RequestID, WorkDir                 string
	ModelBaseURL, ModelAPIKey, ProviderGeneration string
	ModelID                                       string
	SkillDirectory                                string
	ContextText                                   string
	ExpiresAt                                     time.Time
	ToolEnv                                       map[string]string
	CustomEnv                                     map[string]string
	TrajectorySink                                func(context.Context, []byte) error
}
type dshNativeBackend struct {
	cfg       Config
	native    DSHNativeHostConfig
	client    *dshHostClient
	quiescent atomic.Bool
}

var dshNativeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

func NewDSHNativeHostBackend(cfg Config, native DSHNativeHostConfig) (Backend, error) {
	for _, id := range []string{native.WorkspaceID, native.AgentID, native.SessionID, native.RequestID, native.ProviderGeneration} {
		if !dshNativeID.MatchString(id) {
			return nil, errors.New("invalid managed DSH execution identity")
		}
	}
	endpoint, err := url.Parse(native.ModelBaseURL)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || !(endpoint.Scheme == "https" || endpoint.Scheme == "http" && endpoint.Hostname() == "127.0.0.1") || native.ModelAPIKey == "" || strings.ContainsAny(native.ModelAPIKey, "\r\n\x00") {
		return nil, errors.New("invalid managed DSH model configuration")
	}
	if native.Generation < 1 || !protocol.ValidDSHWorkdir(native.WorkDir) || !native.ExpiresAt.After(time.Now()) || native.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
		return nil, errors.New("invalid managed DSH execution scope")
	}
	if native.SkillDirectory != "" && (!filepath.IsAbs(native.SkillDirectory) || filepath.Clean(native.SkillDirectory) != native.SkillDirectory || strings.ContainsRune(native.SkillDirectory, 0)) {
		return nil, errors.New("invalid managed DSH Skill directory")
	}
	if strings.ContainsRune(native.ContextText, 0) || len(native.ContextText) > 1<<20 {
		return nil, errors.New("invalid managed DSH task context")
	}
	if native.Prompt != nil {
		copy, err := native.Prompt.Clone()
		if err != nil || copy.SessionID != native.SessionID || copy.RequestID != native.RequestID {
			return nil, errors.New("managed DSH input identity mismatch")
		}
		native.Prompt = copy
	}
	copied := map[string]string{}
	for key, value := range native.ToolEnv {
		copied[key] = value
	}
	native.ToolEnv = copied
	custom := map[string]string{}
	for key, value := range native.CustomEnv {
		custom[key] = value
	}
	native.CustomEnv = custom
	return &dshNativeBackend{cfg: cfg, native: native, client: &dshHostClient{identity: dshHostIdentity{WorkspaceID: native.WorkspaceID, AgentID: native.AgentID, Generation: native.Generation}, dial: func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/tmp/multica-dsh-host/control.sock")
	}}}, nil
}
func (b *dshNativeBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	if opts.Cwd != b.native.WorkDir || (opts.ResumeSessionID != "" && opts.ResumeSessionID != b.native.SessionID) {
		return nil, errors.New("managed DSH Session or workspace mismatch")
	}
	if b.native.Prompt == nil && strings.TrimSpace(prompt) == "" {
		return nil, errors.New("managed DSH prompt is empty")
	}
	if len(opts.InputImages) > 0 || len(opts.CustomArgs) > 0 || len(opts.ExtraArgs) > 0 {
		return nil, errors.New("native DSH attachment or custom argument mapping is not configured")
	}
	servers, err := buildDshMCPServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, errors.New("invalid managed DSH MCP configuration")
	}
	messages := make(chan Message, 64)
	results := make(chan Result, 1)
	runCtx, cancel := runContext(ctx, opts.Timeout)
	go func() {
		defer cancel()
		defer close(results)
		started := time.Now()
		result := b.executeNative(runCtx, prompt, opts, servers, func(message Message) {
			select {
			case messages <- message:
			case <-runCtx.Done():
			}
		})
		result.DurationMs = time.Since(started).Milliseconds()
		result.SessionID = b.native.SessionID
		close(messages)
		results <- result
	}()
	return &Session{Messages: messages, Result: results}, nil
}
func (b *dshNativeBackend) NativeHostTaskQuiescent() bool { return b.quiescent.Load() }

func (b *dshNativeBackend) taskIdentity() map[string]any {
	return map[string]any{"sessionId": b.native.SessionID, "requestId": b.native.RequestID}
}
func (b *dshNativeBackend) taskState(ctx context.Context) (string, error) {
	raw, err := b.client.call(ctx, "task.status", b.taskIdentity())
	if err != nil {
		return "", err
	}
	var state struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return "", errors.New("invalid native DSH task status")
	}
	switch state.State {
	case "absent", "different", "ready", "preparing", "expired", "cancelled":
		return state.State, nil
	default:
		return "", errors.New("unknown native DSH task status")
	}
}
func (b *dshNativeBackend) admit(ctx context.Context, prompt string, opts ExecOptions, servers []dshMCPServer) error {
	state, err := b.taskState(ctx)
	if err != nil {
		return err
	}
	switch state {
	case "absent":
		mcp := make([]map[string]any, 0, len(servers))
		for _, s := range servers {
			item := map[string]any{"serverName": s.Name, "transport": s.Transport}
			if s.Command != "" {
				item["command"], item["args"], item["env"] = s.Command, s.Args, s.Env
				if s.Cwd != "" {
					item["cwd"] = s.Cwd
				}
			} else {
				item["url"], item["headers"] = s.URL, s.Headers
			}
			if s.ToolCallTimeoutMS > 0 {
				item["toolCallTimeoutMs"] = s.ToolCallTimeoutMS
			}
			mcp = append(mcp, item)
		}
		request := b.taskIdentity()
		request["expiresAt"] = b.native.ExpiresAt.UnixMilli()
		request["toolEnv"] = b.native.ToolEnv
		request["customEnv"] = b.native.CustomEnv
		request["mcpServers"] = mcp
		request["skillDirectory"] = b.native.SkillDirectory
		request["contextText"] = b.native.ContextText
		request["model"] = map[string]string{"baseURL": b.native.ModelBaseURL, "apiKey": b.native.ModelAPIKey, "providerGeneration": b.native.ProviderGeneration}
		raw, err := b.client.call(ctx, "task.bind", request)
		if err != nil {
			return err
		}
		var receipt struct {
			SessionID      string `json:"sessionId"`
			RequestID      string `json:"requestId"`
			SkillDirectory string `json:"skillDirectory"`
			ContextDigest  string `json:"contextDigest"`
		}
		expectedContextDigest := ""
		if b.native.ContextText != "" {
			digest := sha256.Sum256([]byte(b.native.ContextText))
			expectedContextDigest = hex.EncodeToString(digest[:])
		}
		if json.Unmarshal(raw, &receipt) != nil || receipt.SessionID != b.native.SessionID || receipt.RequestID != b.native.RequestID || receipt.SkillDirectory != b.native.SkillDirectory || receipt.ContextDigest != expectedContextDigest {
			return errors.New("invalid native DSH binding receipt")
		}
	case "ready": // Same immutable binding: replay the request ID, never its keys.
	default:
		return errors.New("native DSH task context is not ready for admission")
	}
	if state == "absent" && opts.Model != "" {
		model := opts.Model
		if strings.HasPrefix(model, "multica/") {
			parsed, err := parseDshModelID(model)
			if err != nil {
				return errors.New("invalid native DSH model selection")
			}
			model = parsed.ID
		}
		selection := map[string]any{"sessionId": b.native.SessionID, "provider": "multica", "model": model}
		if opts.ThinkingLevel != "" {
			selection["reasoningEffort"] = opts.ThinkingLevel
		}
		if _, err := b.client.call(ctx, "selectModel", selection); err != nil {
			return err
		}
	}
	request := b.taskIdentity()
	request["mode"] = "queue"
	request["content"] = []map[string]string{{"type": "text", "text": prompt}}
	if b.native.Prompt != nil {
		request["mode"] = b.native.Prompt.Mode
		request["content"] = b.native.Prompt.Content
		if b.native.Prompt.ClientTimeZone != nil {
			request["clientTimeZone"] = *b.native.Prompt.ClientTimeZone
		}
	}
	raw, err := b.client.call(ctx, "prompt", request)
	if err != nil {
		return err
	}
	var receipt struct {
		Accepted bool `json:"accepted"`
	}
	if json.Unmarshal(raw, &receipt) != nil || !receipt.Accepted {
		return errors.New("native DSH prompt was not accepted")
	}
	return nil
}
func (b *dshNativeBackend) executeNative(ctx context.Context, prompt string, opts ExecOptions, servers []dshMCPServer, emit func(Message)) Result {
	failure := func(err error) Result { return Result{Status: "failed", Error: err.Error()} }
	raw, err := b.client.call(ctx, "create", map[string]string{"sessionId": b.native.SessionID, "cwd": b.native.WorkDir})
	if err != nil {
		return failure(err)
	}
	var created struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(raw, &created) != nil || created.SessionID != b.native.SessionID {
		return failure(errors.New("invalid native DSH Session receipt"))
	}
	emit(Message{Type: MessageStatus, Status: "running", SessionID: b.native.SessionID})
	history := dshNativeTaskHistory{requestID: b.native.RequestID}
	var trajectoryHeader json.RawMessage
	opened := false
	err = b.client.follow(ctx, map[string]any{"address": map[string]string{"kind": "session", "sessionId": b.native.SessionID}}, func(raw json.RawMessage) (bool, error) {
		var frame struct {
			Type  string          `json:"type"`
			Event json.RawMessage `json:"event"`
		}
		if json.Unmarshal(raw, &frame) != nil {
			return false, errors.New("invalid native DSH follow frame")
		}
		if !opened {
			snapshot, events, err := readDSHNativeBaseline(ctx, b.client.call, raw, b.native.SessionID, b.native.WorkDir)
			if err != nil {
				return false, err
			}
			trajectoryHeader = snapshot.Header
			for _, event := range events {
				if err := history.accept(event); err != nil {
					return false, err
				}
			}
			opened = true
			if history.terminal != "" {
				return true, nil
			}
			if !history.found {
				if err := b.admit(ctx, prompt, opts, servers); err != nil {
					return false, err
				}
			}
			return false, nil
		}
		switch frame.Type {
		case "heartbeat":
			return false, nil
		case "event":
			if err := history.accept(frame.Event); err != nil {
				return false, err
			}
			if history.found && history.turn == history.ownTurn {
				for _, message := range dshNativeMessages(frame.Event) {
					emit(message)
				}
			}
			return history.terminal != "", nil
		default:
			return false, errors.New("unexpected native DSH follow frame")
		}
	})
	result := Result{}
	if err == nil {
		result, err = history.result(b.native.SessionID)
	}
	cleanupCtx, stopCleanup := context.WithTimeout(context.Background(), 35*time.Second)
	defer stopCleanup()
	if cleanupErr := b.cleanup(cleanupCtx, err != nil); cleanupErr != nil {
		return failure(cleanupErr)
	}
	b.quiescent.Store(true)
	if b.native.TrajectorySink != nil {
		artifactCtx, stopArtifact := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopArtifact()
		// Cancellation can close follow before the native terminal is committed.
		// Resource release has now confirmed idle; read the durable log again.
		if ctx.Err() != nil && history.terminal == "" && opened {
			var restored dshNativeTaskHistory
			restored.requestID = b.native.RequestID
			recoveryErr := b.client.follow(artifactCtx, map[string]any{"address": map[string]string{"kind": "session", "sessionId": b.native.SessionID}}, func(raw json.RawMessage) (bool, error) {
				snapshot, events, readErr := readDSHNativeBaseline(artifactCtx, b.client.call, raw, b.native.SessionID, b.native.WorkDir)
				if readErr != nil {
					return false, readErr
				}
				for _, event := range events {
					if readErr := restored.accept(event); readErr != nil {
						return false, readErr
					}
				}
				trajectoryHeader = snapshot.Header
				return true, nil
			})
			if recoveryErr != nil {
				return failure(errors.New("native DSH cancellation trajectory could not be recovered"))
			}
			history = restored
		}
		if history.found {
			children, childErr := b.childTrajectories(artifactCtx, trajectoryHeader, history.ownEvents, history.childModes)
			if childErr != nil {
				return failure(childErr)
			}
			for _, child := range children {
				if !child.Scope.Closed && result.Status == "completed" {
					result.Status = "failed"
					result.Error = "native DSH child activation was interrupted"
				}
			}
			artifact, artifactErr := history.trajectory(trajectoryHeader, b.native.SessionID, children...)
			if artifactErr != nil {
				return failure(artifactErr)
			}
			if artifactErr = b.native.TrajectorySink(artifactCtx, artifact); artifactErr != nil {
				return failure(errors.New("native DSH task trajectory upload was not confirmed"))
			}
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			status := "cancelled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				status = "timeout"
			}
			return Result{Status: status, Error: "native DSH task stopped after caller cancellation"}
		}
		return failure(err)
	}
	return result
}
func (b *dshNativeBackend) cleanup(ctx context.Context, cancelTask bool) error {
	state, err := b.taskState(ctx)
	if err != nil {
		return errors.New("native DSH task cleanup outcome is unknown")
	}
	if state == "absent" || state == "different" {
		return nil
	}
	if cancelTask {
		if _, err := b.client.call(ctx, "task.cancel", b.taskIdentity()); err != nil {
			return errors.New("native DSH cancellation was not confirmed")
		}
	}
	for {
		raw, err := b.client.call(ctx, "task.release", b.taskIdentity())
		if err == nil {
			var receipt struct {
				Released bool `json:"released"`
			}
			if json.Unmarshal(raw, &receipt) == nil && receipt.Released {
				return nil
			}
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("native DSH task resources remain fenced after cleanup")
		case <-timer.C:
		}
	}
}
func dshNativeMessages(raw json.RawMessage) []Message {
	event, err := decodeDSHNativeEvent(raw)
	if err != nil {
		return nil
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(event.Data, &data) != nil {
		return nil
	}
	text := func(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }
	switch event.Type {
	case "assistant/message":
		var body struct {
			Content []struct{ Type, Text string } `json:"content"`
		}
		if json.Unmarshal(data["message"], &body) != nil {
			return nil
		}
		out := []Message{}
		for _, part := range body.Content {
			kind := MessageText
			if part.Type == "reasoning" {
				kind = MessageThinking
			} else if part.Type != "text" {
				continue
			}
			out = append(out, Message{Type: kind, Content: part.Text})
		}
		return out
	case "tool/call":
		args := map[string]any{}
		_ = json.Unmarshal([]byte(text(data["arguments"])), &args)
		return []Message{{Type: MessageToolUse, CallID: text(data["callId"]), Tool: text(data["name"]), Input: args}}
	case "tool/result":
		var body struct {
			Source struct {
				CallID string `json:"callId"`
			} `json:"source"`
			Content []struct {
				Content json.RawMessage `json:"content"`
			} `json:"content"`
		}
		if json.Unmarshal(data["message"], &body) != nil {
			return nil
		}
		output := ""
		for _, part := range body.Content {
			output += string(part.Content)
		}
		return []Message{{Type: MessageToolResult, CallID: body.Source.CallID, Output: output}}
	}
	return nil
}
