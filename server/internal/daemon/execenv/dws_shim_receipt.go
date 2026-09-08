package execenv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func newDWSSendReceipt(command dwsCommand) (protocol.DingTalkSendReceipt, error) {
	actionID, err := uuid.NewRandom()
	if err != nil {
		return protocol.DingTalkSendReceipt{}, errors.New("create DingTalk send action id")
	}
	// Never hash credentials or command-line transport options into evidence.
	// Normalize the official text/Markdown shortcuts to the same content key.
	payload := make(map[string]string)
	for _, key := range []string{"content", "text", "markdown", "title", "msg-type", "file", "file-path", "media-id", "contact-id", "latitude", "longitude", "location-name", "map-thumbnail-url", "at-all", "at-open-dingtalk-ids", "at-mobiles", "at-user-ids", "ref-msg-id", "message-id", "ref-sender"} {
		if value, exists := command.values[key]; exists {
			name := key
			if key == "text" || key == "markdown" {
				name = "content"
			}
			payload[name] = value
		}
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	key := command.values["idempotency-key"]
	if key == "" {
		key = command.values["uuid"]
	}
	return protocol.DingTalkSendReceipt{
		ClientActionID: actionID.String(), State: "pending", OpenConversationID: command.conversationID,
		RecipientOpenDingTalkID: command.values["open-dingtalk-id"],
		IdempotencyKey:          key, PayloadHash: hex.EncodeToString(digest[:]),
	}, nil
}

func updateDWSSendReceipt(receipt protocol.DingTalkSendReceipt, output string, runErr error) protocol.DingTalkSendReceipt {
	receipt.State = "unknown"
	if runErr != nil {
		receipt.ErrorCode = "dws_command_failed"
	}
	// The provider parsers require a successful envelope. An openTaskId is
	// only acceptance; success=true alone or bare message IDs prove nothing.
	if sent, err := dwsclient.ParseSendResult([]byte(output)); err == nil {
		receipt.State, receipt.OpenTaskID = "accepted", sent.OpenTaskID
		receipt.ErrorCode = ""
	} else {
		var rejected *dwsclient.SendRejectedError
		if errors.As(err, &rejected) {
			receipt.State, receipt.ErrorCode = "failed", rejected.Code
			return receipt
		}
	}
	if status, err := dwsclient.ParseSendStatus([]byte(output)); err == nil {
		if status.State == "delivered" {
			receipt.State = "delivered"
			receipt.OpenConversationID = status.OpenConversationID
			receipt.OpenMessageID = status.OpenMessageID
			receipt.ErrorCode = ""
		} else if status.State == "failed" {
			receipt.State, receipt.ErrorCode = "failed", status.ErrorCode
		}
	}
	return receipt
}

func reportDWSSendReceipt(getenv func(string) string, client *http.Client, receipt protocol.DingTalkSendReceipt) error {
	token := getenv("MULTICA_TOKEN")
	if !strings.HasPrefix(token, "mat_") {
		return errors.New("DingTalk send receipt requires a task-scoped token")
	}
	taskID, err := uuid.Parse(getenv("MULTICA_TASK_ID"))
	if err != nil {
		return errors.New("DingTalk send receipt requires a task id")
	}
	workspaceID, err := uuid.Parse(getenv("MULTICA_WORKSPACE_ID"))
	if err != nil {
		return errors.New("DingTalk send receipt requires a workspace id")
	}
	endpoint, err := url.Parse(getenv("MULTICA_SERVER_URL"))
	if err != nil || endpoint == nil || endpoint.Host == "" || endpoint.User != nil ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("DingTalk send receipt requires a valid task server URL")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/tasks/" + taskID.String() + "/dingtalk-send-receipts"
	endpoint.RawPath = ""
	body, err := json.Marshal(receipt)
	if err != nil {
		return errors.New("encode DingTalk send receipt")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return errors.New("create DingTalk send receipt request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Workspace-ID", workspaceID.String())
	request.Header.Set("Content-Type", "application/json")
	// Copy the client so redirect policy never mutates a shared caller. The
	// task token must not follow a redirect to another service or login page.
	if client == nil {
		client = http.DefaultClient
	}
	transport := *client
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := transport.Do(request)
	if err != nil {
		return errors.New("DingTalk send receipt request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("DingTalk send receipt rejected (HTTP %d)", response.StatusCode)
	}
	var result struct {
		Accepted bool `json:"accepted"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result); err != nil || !result.Accepted {
		return errors.New("DingTalk send receipt was not acknowledged")
	}
	return nil
}
