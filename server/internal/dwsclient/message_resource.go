package dwsclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// MessageResource is one resource the provider lists in a message's own
// structured resources field.
type MessageResource struct {
	ID     string // resourceId
	IDType string // fileId or mediaId
	Type   string // file, image, video, voice, ...
}

// QuotedMessage identifies the message a reply quotes. Its resources are
// deliberately absent: a nested resource list is never proof of what the
// quoting message carries, and the quoted message must be read on its own.
type QuotedMessage struct {
	MessageID            string
	ConversationID       string
	SenderOpenDingTalkID string
}

// MessageResources is the provider's exact record of one message: its
// identity, its sender and only its own structured resources. Text notation
// (fileId: / mediaId=), links and derived resourceRefs are never read.
type MessageResources struct {
	MessageID            string
	ConversationID       string
	SenderOpenDingTalkID string
	Resources            []MessageResource
	Quoted               *QuotedMessage
}

// MessageFile is a downloaded message file. The signed download URL and the
// provider's request headers stay inside DownloadMessageFile.
type MessageFile struct {
	Name         string
	DeclaredSize int64
	ContentType  string
	Data         []byte
}

var (
	ErrMessageFileTooLarge    = errors.New("DWS message file exceeds the size limit")
	ErrMessageFileUnavailable = errors.New("DWS message file download is unavailable")
	// ErrMessageUnverified: the provider's record is not the exact message
	// asked for (identity, conversation, sender or resource list); retrying
	// the same read does not change that.
	ErrMessageUnverified = errors.New("DWS message resources could not be verified")
)

const maxMessageResourceIDBytes = 256

// ReadMessageResources reads the exact message as the directory's identity
// and returns its own structured resources.
func (c CLI) ReadMessageResources(ctx context.Context, dir, conversationID, messageID string) (MessageResources, error) {
	if !validResourceToken(conversationID) || !validResourceToken(messageID) {
		return MessageResources{}, ErrMessageUnverified
	}
	raw, err := c.messageOp(ctx, dir, []string{"chat", "message", "list-by-ids", "--msg-ids", messageID, "--format", "json"}, func(client *dws.Client) ([]byte, error) {
		// Not MessagesByIDsOutput: its resourceRefs add IDs parsed from text.
		raw, err := client.CallRaw(ctx, dws.ServerIM, "list_messages_by_ids", map[string]any{"openMsgIds": []string{messageID}})
		if err != nil {
			return nil, commandFailed(ctx, "DWS message resource read failed", err)
		}
		return sdkBounded(raw)
	})
	if err != nil {
		return MessageResources{}, err
	}
	return ParseMessageResources(raw, conversationID, messageID)
}

// ParseMessageResources verifies one exact message of a list_messages_by_ids
// response and projects its identity, sender and own resources.
func ParseMessageResources(raw []byte, conversationID, messageID string) (MessageResources, error) {
	if !validResourceToken(conversationID) || !validResourceToken(messageID) {
		return MessageResources{}, ErrMessageUnverified
	}
	message, err := parseExactMessage(raw, conversationID, messageID)
	if err != nil {
		return MessageResources{}, ErrMessageUnverified
	}
	sender, err := parseMessageSender(raw, conversationID, messageID)
	if err != nil {
		return MessageResources{}, ErrMessageUnverified
	}
	var body struct {
		Resources []struct {
			ID     string `json:"resourceId"`
			IDType string `json:"resourceIdType"`
			Type   string `json:"resourceType"`
		} `json:"resources"`
		Quoted *struct {
			MessageID            string `json:"openMessageId"`
			LegacyMessageID      string `json:"messageId"`
			ConversationID       string `json:"openConversationId"`
			LegacyConversationID string `json:"conversationId"`
			SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
		} `json:"quotedMessage"`
	}
	if json.Unmarshal(message, &body) != nil {
		return MessageResources{}, ErrMessageUnverified
	}
	out := MessageResources{MessageID: messageID, ConversationID: conversationID, SenderOpenDingTalkID: sender}
	seen := map[string]MessageResource{}
	for _, r := range body.Resources {
		resource := MessageResource{ID: strings.TrimSpace(r.ID), IDType: strings.TrimSpace(r.IDType), Type: strings.ToLower(strings.TrimSpace(r.Type))}
		if !validResourceToken(resource.ID) || (resource.IDType != "fileId" && resource.IDType != "mediaId") || !validResourceToken(resource.Type) {
			return MessageResources{}, ErrMessageUnverified
		}
		if prior, ok := seen[resource.ID]; ok {
			if prior != resource {
				// One ID with two meanings cannot name a single resource.
				return MessageResources{}, ErrMessageUnverified
			}
			continue
		}
		seen[resource.ID] = resource
		out.Resources = append(out.Resources, resource)
	}
	if q := body.Quoted; q != nil {
		quoted := QuotedMessage{MessageID: firstNonEmptyString(q.MessageID, q.LegacyMessageID), ConversationID: firstNonEmptyString(q.ConversationID, q.LegacyConversationID), SenderOpenDingTalkID: strings.TrimSpace(q.SenderOpenDingTalkID)}
		if quoted.ConversationID == "" {
			// Quoted records often omit their conversation: it is the enclosing one.
			quoted.ConversationID = conversationID
		}
		if quoted.ConversationID != conversationID {
			return MessageResources{}, ErrMessageUnverified
		}
		if validResourceToken(quoted.MessageID) {
			out.Quoted = &quoted
		}
	}
	return out, nil
}

// DownloadMessageFile resolves a fileId through DingTalk Drive as the
// directory's identity and downloads at most maxBytes. Callers must have
// proven fileID is an own resource of an authorized message first: Drive
// itself checks only the identity's access to the file.
func (c CLI) DownloadMessageFile(ctx context.Context, dir, fileID string, maxBytes int64) (MessageFile, error) {
	if !validResourceToken(fileID) || maxBytes <= 0 {
		return MessageFile{}, ErrMessageFileUnavailable
	}
	raw, err := c.messageOp(ctx, dir, []string{"drive", "download", "--node", fileID, "--url-only", "--format", "json"}, func(client *dws.Client) ([]byte, error) {
		payload, failure, err := sdkTool(ctx, client, dws.ServerDrive, "download_file", map[string]any{"fileId": fileID})
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return nil, ErrMessageFileUnavailable
		}
		return sdkBounded(payload)
	})
	if err != nil {
		if ctx.Err() != nil {
			return MessageFile{}, fmt.Errorf("%w: %w", ErrMessageFileUnavailable, ctx.Err())
		}
		return MessageFile{}, ErrMessageFileUnavailable
	}
	target, err := parseFileDownloadTarget(raw)
	if err != nil {
		return MessageFile{}, err
	}
	if target.size > maxBytes {
		return MessageFile{}, ErrMessageFileTooLarge
	}
	return fetchMessageFile(ctx, target, maxBytes)
}

type fileDownloadTarget struct {
	url     *url.URL
	headers map[string]string
	name    string
	size    int64
}

func parseFileDownloadTarget(raw []byte) (fileDownloadTarget, error) {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return fileDownloadTarget{}, ErrMessageFileUnavailable
	}
	if ok, present := value["success"].(bool); present && !ok {
		return fileDownloadTarget{}, ErrMessageFileUnavailable
	}
	for depth := 0; depth < 4; depth++ {
		next, ok := value["result"].(map[string]any)
		if !ok {
			next, ok = value["data"].(map[string]any)
		}
		if !ok {
			break
		}
		value = next
	}
	text := func(keys ...string) string {
		for _, key := range keys {
			if s, ok := value[key].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	parsed, err := url.Parse(text("downloadUrl", "resourceUrl", "url"))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" {
		return fileDownloadTarget{}, ErrMessageFileUnavailable
	}
	target := fileDownloadTarget{url: parsed, headers: map[string]string{}, name: text("fileName", "name")}
	if headers, ok := value["headers"].(map[string]any); ok {
		for key, v := range headers {
			if s, ok := v.(string); ok && strings.TrimSpace(key) != "" {
				target.headers[key] = s
			}
		}
	}
	switch size := value["fileSize"].(type) {
	case float64:
		target.size = int64(size)
	case string:
		target.size, _ = strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	}
	return target, nil
}

func defaultMessageFileHTTPClient() *http.Client { return &http.Client{Timeout: 30 * time.Second} }

// messageFileHTTPClient lets tests trust a local TLS server.
var messageFileHTTPClient = defaultMessageFileHTTPClient

func fetchMessageFile(ctx context.Context, target fileDownloadTarget, maxBytes int64) (MessageFile, error) {
	client := *messageFileHTTPClient()
	origin := target.url.Host
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != "https" || req.URL.User != nil {
			return ErrMessageFileUnavailable
		}
		if req.URL.Host != origin {
			// Provider headers authorize the original host only.
			for key := range target.headers {
				req.Header.Del(key)
			}
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.url.String(), nil)
	if err != nil {
		return MessageFile{}, ErrMessageFileUnavailable
	}
	for key, value := range target.headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		// *url.Error prints the signed URL; keep only the cause class.
		if ctx.Err() != nil {
			return MessageFile{}, fmt.Errorf("%w: %w", ErrMessageFileUnavailable, ctx.Err())
		}
		return MessageFile{}, ErrMessageFileUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return MessageFile{}, ErrMessageFileUnavailable
	}
	if resp.ContentLength > maxBytes {
		return MessageFile{}, ErrMessageFileTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return MessageFile{}, ErrMessageFileUnavailable
	}
	if int64(len(data)) > maxBytes {
		return MessageFile{}, ErrMessageFileTooLarge
	}
	contentType := ""
	if mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil {
		contentType = mediaType
	}
	return MessageFile{Name: target.name, DeclaredSize: target.size, ContentType: contentType, Data: data}, nil
}

func validResourceToken(value string) bool {
	if value == "" || len(value) > maxMessageResourceIDBytes || value != strings.TrimSpace(value) {
		return false
	}
	for _, r := range value {
		// Provider IDs are opaque base64-like tokens ('/', '+', '=' occur);
		// whitespace, controls and list separators never do.
		if r <= ' ' || r == 0x7f || r == ',' {
			return false
		}
	}
	return true
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
