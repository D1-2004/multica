package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"
)

// A protocol peer on net.Pipe tests the production Backend path without a
// local Host, network service, model or database.
type nativeBackendPeer struct {
	mu             sync.Mutex
	calls          map[string]int
	promptRequests []json.RawMessage
	state          string
	baseline       []json.RawMessage
	live           []json.RawMessage
	prompted       chan struct{}
	hold           bool
	receipt        map[string]string
}

func (p *nativeBackendPeer) client() *dshHostClient {
	return &dshHostClient{dial: func(ctx context.Context) (net.Conn, error) {
		local, remote := net.Pipe()
		go func() {
			defer remote.Close()
			line, err := bufio.NewReader(remote).ReadBytes('\n')
			if err != nil {
				return
			}
			var request struct {
				Method  string                     `json:"method"`
				Request map[string]json.RawMessage `json:"request"`
			}
			if json.Unmarshal(line, &request) != nil {
				return
			}
			p.mu.Lock()
			p.calls[request.Method]++
			p.mu.Unlock()
			write := func(value any) bool {
				return json.NewEncoder(remote).Encode(map[string]any{"ok": true, "value": value}) == nil
			}
			switch request.Method {
			case "create":
				write(map[string]string{"sessionId": "session"})
			case "task.status":
				p.mu.Lock()
				state := p.state
				p.mu.Unlock()
				write(map[string]string{"state": state})
			case "task.bind":
				p.mu.Lock()
				p.state = "ready"
				p.mu.Unlock()
				if p.receipt != nil {
					write(p.receipt)
				} else {
					write(map[string]string{"sessionId": "session", "requestId": "mine"})
				}
			case "prompt":
				p.mu.Lock()
				encoded, _ := json.Marshal(request.Request)
				p.promptRequests = append(p.promptRequests, encoded)
				p.mu.Unlock()
				write(map[string]bool{"accepted": true})
				close(p.prompted)
			case "task.cancel":
				write(map[string]bool{"accepted": true})
			case "task.release":
				p.mu.Lock()
				p.state = "absent"
				p.mu.Unlock()
				write(map[string]bool{"released": true})
			case "page":
				write(map[string]any{"records": nativeTestRecords(p.baseline[:4]), "hasMore": false})
			case "follow":
				events := p.baseline
				more := len(events) > 4
				if more {
					events = events[4:]
				}
				if !write(map[string]any{"type": "snapshot", "header": map[string]any{"version": 3, "id": "session", "cwd": "/mnt/multica/workspaces/session", "isSeeded": false, "delegationDepth": 0}, "cursor": len(p.baseline) - 1, "records": nativeTestRecords(events), "hasMore": more}) {
					return
				}
				if len(p.baseline) > 0 {
					return
				}
				select {
				case <-p.prompted:
				case <-ctx.Done():
					return
				}
				for _, event := range p.live {
					if !write(map[string]any{"type": "event", "event": event}) {
						return
					}
				}
				if p.hold {
					<-ctx.Done()
				}
			}
		}()
		return local, nil
	}}
}
func TestDSHNativeBackendRequiresSkillAndContextReceiptBeforePrompt(t *testing.T) {
	const directory, brief = "/scratch/task/skills", "Current task {{literal}}"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(brief)))
	for _, mode := range []string{"valid", "missing-skills", "wrong-skills", "missing-context", "wrong-context"} {
		t.Run(mode, func(t *testing.T) {
			receipt := map[string]string{"sessionId": "session", "requestId": "mine", "skillDirectory": directory, "contextDigest": digest}
			switch mode {
			case "missing-skills":
				delete(receipt, "skillDirectory")
			case "wrong-skills":
				receipt["skillDirectory"] = "/scratch/previous/skills"
			case "missing-context":
				delete(receipt, "contextDigest")
			case "wrong-context":
				receipt["contextDigest"] = fmt.Sprintf("%x", sha256.Sum256([]byte("previous task")))
			}
			peer := &nativeBackendPeer{calls: map[string]int{}, state: "absent", prompted: make(chan struct{}), receipt: receipt}
			backend := &dshNativeBackend{native: DSHNativeHostConfig{SessionID: "session", RequestID: "mine", SkillDirectory: directory, ContextText: brief}, client: peer.client()}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := backend.admit(ctx, "fixture", ExecOptions{}, nil)
			peer.mu.Lock()
			defer peer.mu.Unlock()
			if mode == "valid" {
				if err != nil || peer.calls["prompt"] != 1 {
					t.Fatalf("affirmative receipt did not admit prompt: %v", err)
				}
			} else if err == nil || peer.calls["prompt"] != 0 {
				t.Fatal("unconfirmed task resources admitted a prompt")
			}
		})
	}
}
func TestDSHNativeBackendExecutesOnHostAndReplaysCompletedRequest(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "retry"}[retry], func(t *testing.T) {
			peer := &nativeBackendPeer{calls: map[string]int{}, state: "absent", prompted: make(chan struct{}), live: nativeTestTurn(0, 1, "mine", "own output", "completed")}
			if retry {
				peer.baseline = append(nativeTestTurn(0, 1, "mine", "own output", "completed"), nativeTestTurn(4, 2, "other", "foreign output", "completed")...)
			}
			backend := &dshNativeBackend{native: DSHNativeHostConfig{SessionID: "session", RequestID: "mine", WorkDir: "/mnt/multica/workspaces/session", ExpiresAt: time.Now().Add(time.Minute)}, client: peer.client()}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			session, err := backend.Execute(ctx, "fixture", ExecOptions{Cwd: backend.native.WorkDir})
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
			}
			result := <-session.Result
			if result.Status != "completed" || result.Output != "own output" || result.SessionID != "session" {
				t.Fatalf("wrong native result: %+v", result)
			}
			peer.mu.Lock()
			defer peer.mu.Unlock()
			if retry {
				if peer.calls["prompt"] != 0 || peer.calls["task.bind"] != 0 || peer.calls["page"] != 1 {
					t.Fatal("completed request was resubmitted or not fully read")
				}
			} else {
				if peer.calls["prompt"] != 1 || peer.calls["task.bind"] != 1 || peer.calls["task.release"] != 1 {
					t.Fatal("native task did not traverse admission and cleanup")
				}
			}
		})
	}
}
func TestDSHNativeBackendCancelsItsRequestOnEOFAndCallerCancellation(t *testing.T) {
	for _, hold := range []bool{false, true} {
		t.Run(map[bool]string{false: "EOF", true: "cancel"}[hold], func(t *testing.T) {
			peer := &nativeBackendPeer{calls: map[string]int{}, state: "absent", prompted: make(chan struct{}), live: nativeTestTurn(0, 1, "mine", "partial", "completed")[:3], hold: hold}
			backend := &dshNativeBackend{native: DSHNativeHostConfig{SessionID: "session", RequestID: "mine", WorkDir: "/mnt/multica/workspaces/session", ExpiresAt: time.Now().Add(time.Minute)}, client: peer.client()}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			session, err := backend.Execute(ctx, "fixture", ExecOptions{Cwd: backend.native.WorkDir})
			if err != nil {
				t.Fatal(err)
			}
			for message := range session.Messages {
				if hold && message.Type == MessageText {
					cancel()
				}
			}
			result := <-session.Result
			if result.Status == "completed" {
				t.Fatal("partial native request falsely completed")
			}
			if hold && result.Status != "cancelled" {
				t.Fatalf("cancellation result=%s", result.Status)
			}
			peer.mu.Lock()
			defer peer.mu.Unlock()
			if peer.calls["task.cancel"] != 1 || peer.calls["task.release"] != 1 {
				t.Fatal("request cancellation did not wait for resource release")
			}
		})
	}
}

func TestDSHNativeBackendTransportsFullPrompt(t *testing.T) {
	for _, mode := range []string{"queue", "steer"} {
		t.Run(mode, func(t *testing.T) {
			text := "原始输入"
			data := "aGVsbG8="
			media := "image/png"
			receipt := "upload-receipt"
			zone := "Asia/Shanghai"
			prompt := &protocol.DSHNativePrompt{SessionID: "session-31e58f19-8669-42f3-98f6-01bc71aa8ad0", RequestID: "91675c65-a8e3-4b25-85fc-5e82081af8af", Mode: mode, Content: []protocol.DSHNativePromptPart{{Type: "text", Text: &text}, {Type: "image", MediaType: &media, Data: &data}, {Type: "file", ReceiptID: &receipt}}, ClientTimeZone: &zone}
			native := DSHNativeHostConfig{WorkspaceID: "workspace", AgentID: "employee", Generation: 1, SessionID: prompt.SessionID, RequestID: prompt.RequestID, WorkDir: "/mnt/multica/workspaces/" + prompt.SessionID, ModelBaseURL: "https://model.example", ModelAPIKey: "fixture", ProviderGeneration: "fixture", ExpiresAt: time.Now().Add(time.Hour), Prompt: prompt}
			created, err := NewDSHNativeHostBackend(Config{}, native)
			if err != nil {
				t.Fatal(err)
			}
			backend := created.(*dshNativeBackend)
			expected, err := prompt.Clone()
			if err != nil {
				t.Fatal(err)
			}
			data = "mutated"
			zone = "UTC"
			peer := &nativeBackendPeer{calls: map[string]int{}, state: "ready", prompted: make(chan struct{})}
			backend.client = peer.client()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := backend.admit(ctx, "generated platform summary must not execute", ExecOptions{}, nil); err != nil {
				t.Fatal(err)
			}
			peer.mu.Lock()
			defer peer.mu.Unlock()
			if len(peer.promptRequests) != 1 {
				t.Fatal("missing native prompt")
			}
			actual, err := protocol.DecodeDSHNativePrompt(peer.promptRequests[0])
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatal("native wire input changed")
			}
			native.Prompt.RequestID = "dd996f77-e2fd-4a16-84d9-1032b55a05f1"
			if _, err := NewDSHNativeHostBackend(Config{}, native); err == nil {
				t.Fatal("foreign request admitted")
			}
		})
	}
}
