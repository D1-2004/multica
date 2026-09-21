package dwsclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type A2UIAnnotation struct {
	SurfaceID   string `json:"surfaceId"`
	ComponentID string `json:"componentId"`
	Type        string `json:"type"`
}

type A2UISendRequest struct {
	ConversationID, BizID, RequestID, Summary string
	Messages                                  []string
}
type A2UIReceipt struct {
	BizID          string `json:"bizId"`
	CardInstanceID int64  `json:"cardInstanceId"`
}

// SendA2UI supplies stable tracing/business IDs. They are NOT evidence of send
// idempotency: a caller must reconcile unknown outcomes instead of resending.
func (c CLI) SendA2UI(ctx context.Context, dir string, in A2UISendRequest) (A2UIReceipt, error) {
	if in.ConversationID == "" || in.BizID == "" || in.RequestID == "" || in.Summary == "" || len(in.Messages) == 0 {
		return A2UIReceipt{}, errors.New("incomplete A2UI send request")
	}
	body, err := json.Marshal(in.Messages)
	if err != nil {
		return A2UIReceipt{}, err
	}
	raw, err := c.messageCommand(ctx, dir, []string{"chat", "+messages-send", "--as", "user", "--chat-id", in.ConversationID, "--msg-type", "a2ui", "--a2ui-messages", string(body), "--biz-card-id", in.BizID, "--request-id", in.RequestID, "--card-summary", in.Summary, "--yes", "--format", "json"})
	if err != nil {
		return A2UIReceipt{}, err
	}
	return parseA2UIReceipt(raw)
}
func parseA2UIReceipt(raw []byte) (A2UIReceipt, error) {
	var response struct {
		OK     bool `json:"ok"`
		Result struct {
			Success bool        `json:"success"`
			Result  A2UIReceipt `json:"result"`
		} `json:"result"`
	}
	if len(raw) > MaxResponseBytes || json.Unmarshal(raw, &response) != nil || !response.OK || !response.Result.Success || response.Result.Result.BizID == "" || response.Result.Result.CardInstanceID == 0 {
		return A2UIReceipt{}, errors.New("A2UI send outcome is unconfirmed")
	}
	return response.Result.Result, nil
}
func (c CLI) UpdateA2UI(ctx context.Context, dir, bizID, status string, messages []string, annotations []A2UIAnnotation) error {
	valid := map[string]bool{"INPUTTING": true, "CONFIRMED": true, "EXECUTING": true, "FINISH": true, "ERROR": true, "ABORTED": true, "TIMEOUT": true}
	if bizID == "" || !valid[status] || len(messages) == 0 {
		return errors.New("invalid A2UI update")
	}
	body, _ := json.Marshal(messages)
	if annotations == nil {
		annotations = []A2UIAnnotation{}
	}
	annotationJSON, _ := json.Marshal(annotations)
	raw, err := c.messageCommand(ctx, dir, []string{"chat", "message", "update-a2ui-card", "--biz-id", bizID, "--content", string(body), "--flow-status", status, "--a2ui-annotations", string(annotationJSON), "--format", "json"})
	if err != nil {
		return err
	}
	var result struct {
		Success bool `json:"success"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Success {
		return errors.New("A2UI update was not confirmed")
	}
	return nil
}

// ConsumeCardEvents uses an isolated authenticated directory. It exposes ready
// only after the CLI confirms subscription; both output pipes are drained and
// raw stderr is never logged because it may contain credential diagnostics.
// The owning service supplies distributed ownership and reconnect backoff.
func (c CLI) ConsumeCardEvents(ctx context.Context, dir string, ready func(), consume func([]byte) error) error {
	if ready == nil || consume == nil {
		return errors.New("card consumer callbacks are required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.path(), "event", "consume", "user_card_action_triggered", "-f", "ndjson")
	cmd.Env = c.commandEnv(dir, nil)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return commandFailed(ctx, "start DWS card consumer", err)
	}
	var workers sync.WaitGroup
	var once sync.Once
	var consumerErr error
	workers.Add(2)
	go func() {
		defer workers.Done()
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 4096), MaxResponseBytes)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "[event] ready ") {
				once.Do(ready)
			}
		}
		// A stopped stderr reader must not leave the child blocked on a full pipe.
		if scanner.Err() != nil {
			cancel()
			_, _ = io.Copy(io.Discard, stderr)
		}
	}()
	go func() {
		defer workers.Done()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), MaxResponseBytes)
		for scanner.Scan() {
			if err := consume(append([]byte(nil), scanner.Bytes()...)); err != nil {
				consumerErr = err
				cancel()
				_, _ = io.Copy(io.Discard, stdout)
				return
			}
		}
		if scanner.Err() != nil {
			consumerErr = errors.New("DWS card event exceeds stream limit")
			cancel()
		}
	}()
	workers.Wait()
	err = cmd.Wait()
	if consumerErr != nil {
		return consumerErr
	}
	return commandFailed(ctx, "DWS card consumer stopped", err)
}

// VerifyInternalGroup requires the live group to belong to the authenticated
// organization's profile; ordinary/cross-organization groups are excluded.
func (c CLI) VerifyInternalGroup(ctx context.Context, dir, cid string) (string, error) {
	raw, err := c.messageCommand(ctx, dir, []string{"chat", "+conversation-info", "--group", cid, "--format", "json"})
	if err != nil {
		return "", err
	}
	var info struct {
		Success bool `json:"success"`
		Result  struct {
			Conversation struct {
				ID       string `json:"openConversationId"`
				CorpID   string `json:"corpId"`
				CorpName string `json:"corpName"`
				Single   bool   `json:"singleChat"`
			} `json:"conversationInfo"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &info) != nil || !info.Success || info.Result.Conversation.ID != cid || info.Result.Conversation.Single || info.Result.Conversation.CorpID == "" || info.Result.Conversation.CorpName == "" {
		return "", errors.New("user decision requires an enterprise internal group")
	}
	raw, err = c.messageCommand(ctx, dir, []string{"profile", "list", "--format", "json"})
	if err != nil {
		return "", err
	}
	var profiles struct {
		Success  bool   `json:"success"`
		Current  string `json:"currentProfile"`
		Profiles []struct {
			ID     string `json:"profile"`
			CorpID string `json:"corpId"`
		} `json:"profiles"`
	}
	if json.Unmarshal(raw, &profiles) != nil || !profiles.Success {
		return "", errors.New("could not verify sender organization")
	}
	for _, p := range profiles.Profiles {
		if p.ID == profiles.Current && p.CorpID == info.Result.Conversation.CorpID {
			return p.CorpID, nil
		}
	}
	return "", errors.New("group is outside the sender organization")
}
